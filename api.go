package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/go-connections/nat"
	"github.com/fsouza/go-dockerclient"
)

type Environment struct {
	Name      string     `json:"name"`
	Container *Container `json:"container"`
	Type      string     `json:"type"`
}

type UserImage struct {
	Name    string
	Aliases []string
	Labels  map[string]string
}

type BaseImage struct {
	Name        string
	Description string
	Tags        []*BaseImageTag
}

type ByName []UserImage

func (a ByName) Len() int           { return len(a) }
func (a ByName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByName) Less(i, j int) bool { return a[i].Name < a[j].Name }

type BaseImageTag struct {
	Name      string
	Pulled    bool
	Preferred bool
}

type CreateOpts struct {
	Name          string
	Ports         []string
	ExistingPorts []Port
	Volumes       []string
	ProjectDir    string
	ForceBuild    bool
	Build         BuildOpts
}

type BuildOpts struct {
	Username  string
	UID, GID  int
	Image     ImageOpts
	ForcePull bool
	TimeZone  string
}

type ImageOpts struct {
	Type    string
	Version string
	Image   string
}

var dockerOrg = "dockdev"

func ParsePorts(portSpecs []string) ([]Port, error) {
	ports := make([]Port, 0)

	exposed, bindings, err := nat.ParsePortSpecs(portSpecs)
	if err != nil {
		return ports, err
	}

	for port, _ := range exposed {
		portParts := strings.Split(string(port), "/")
		contPort, proto := portParts[0], portParts[1]
		cp, _ := strconv.Atoi(contPort)
		if cp == 22 && proto == "tcp" {
			return ports, errors.New("bad container port, 22 reserved for ssh")
		}
		for _, binding := range bindings[port] {
			if strings.Contains(binding.HostPort, "-") {
				return ports, errors.New("dynamic port ranges not supported (yet)")
			}
			hp, _ := strconv.Atoi(binding.HostPort)

			ports = append(ports, Port{
				binding.HostIP,
				int64(hp),
				int64(cp),
				proto,
			})
		}
	}

	return ports, nil
}

func DestroyEnvironment(dc DockerClient, sc SystemClient, envName string) error {

	logrus.Debugf("Stopping environment")
	env, err := EnsureStopped(dc, sc, envName)
	if err != nil {
		return err
	}

	if env.Container != nil {
		err = dc.RemoveContainer(env.Container.Name)
		if err != nil {
			return err
		}
	}

	logrus.Debugf("Removing local environment directory")
	err = sc.RemoveEnvironmentDir(env.Name)
	if err != nil {
		return err
	}

	return nil
}

func RebuildEnvironment(dc DockerClient, sc SystemClient, co CreateOpts, output *os.File) error {
	env, err := GetEnvironment(dc, sc, co.Name)
	if err != nil {
		return err
	}

	// fmt.Println(co)
	// fmt.Println(env)

	// TODO: check for re-specifying the same port
	logrus.Debugf("Merge in ports")
	ports := make([]Port, 0)
	for _, port := range env.Container.Ports {
		if port.ContainerPort == 22 {
			continue
		}
		if port.HostPort > 30000 {
			port.HostPort = 0
		}
		ports = append(ports, port)
	}
	co.ExistingPorts = ports

	// TODO: check for re-specifying the same volume
	logrus.Debugf("Merge in volumes")
	dockerContainer, err := dc.InspectContainer(env.Container.Name)
	if err != nil {
		return err
	}

	volumes := co.Volumes
	for _, mount := range dockerContainer.Mounts {
		if mount.Destination == fmt.Sprintf("/home/%s", sc.Username()) {
			continue
		}

		volumes = append(volumes, fmt.Sprintf("%s:%s", mount.Source, mount.Destination))
	}
	co.Volumes = volumes

	logrus.Debugf("Set image opts")
	co.Build.Image = ImageOpts{
		Image: env.Container.Labels["skeg.io/image/base"],
	}
	// fmt.Println(co)

	logrus.Debugf("Stopping environment")
	_, err = EnsureStopped(dc, sc, env.Name)
	if err != nil {
		return err
	}

	if env.Container != nil {
		err = dc.RemoveContainer(env.Container.Name)
		if err != nil {
			return err
		}
	}

	return CreateEnvironment(dc, sc, co, output)
}

func CreateEnvironment(dc DockerClient, sc SystemClient, co CreateOpts, output *os.File) error {
	ports, err := ParsePorts(co.Ports)
	if err != nil {
		return err
	}
	ports = append(ports, Port{
		"", 0, 22, "tcp",
	})
	ports = append(ports, co.ExistingPorts...)

	logrus.Debugf("Ensuring SSH key is present")
	key, err := sc.EnsureSSHKey()
	if err != nil {
		return err
	}

	var imageName string
	userImages, err := UserImages(dc, sc, co.Build.Image)
	if co.ForceBuild || len(userImages) == 0 {
		logrus.Debugf("Building customized docker image")
		imageName, err = BuildImage(dc, co.Build, output)
		if err != nil {
			return err
		}
	} else {
		imageName = userImages[0].Name
		logrus.Infof("Using existing image %s", imageName)
	}

	logrus.Debugf("Preparing local environment directory")
	path, err := sc.EnsureEnvironmentDir(co.Name, key)
	if err != nil {
		return err
	}

	logrus.Debugf("Creating container")
	volumes := co.Volumes
	volumes = append(volumes, fmt.Sprintf("%s:/home/%s", path, sc.Username()))
	workdirParts := strings.Split(co.ProjectDir, string(os.PathSeparator))
	if len(co.ProjectDir) > 0 {
		volumes = append(volumes, fmt.Sprintf("%s:/home/%s/%s", co.ProjectDir, sc.Username(), workdirParts[len(workdirParts)-1]))
	}

	containerName := fmt.Sprintf("%s_%s", CONT_PREFIX, co.Name)
	ccont := CreateContainerOpts{
		Name:     containerName,
		Image:    imageName,
		Hostname: co.Name,
		Ports:    ports,
		Volumes:  volumes,
	}
	err = dc.CreateContainer(ccont)
	if err != nil {
		return err
	}

	logrus.Debugf("Starting container")
	_, err = EnsureRunning(dc, sc, co.Name)
	if err != nil {
		return err
	}

	return nil
}

func CreateNewEnvironment(dc DockerClient, sc SystemClient, co CreateOpts, output *os.File) error {
	logrus.Debugf("Checking if environment already exists")
	envs, err := Environments(dc, sc)
	if err != nil {
		return err
	}
	if _, ok := envs[co.Name]; ok {
		return fmt.Errorf("Environment %s already exists", co.Name)
	}

	return CreateEnvironment(dc, sc, co, output)
}

func EnsureRunning(dc DockerClient, sc SystemClient, envName string) (Environment, error) {
	var env Environment

	envs, err := Environments(dc, sc)
	if err != nil {
		return env, err
	}
	env, ok := envs[envName]

	if !ok {
		return env, fmt.Errorf("Environment %s doesn't exist.", envName)
	}

	if env.Container != nil && !env.Container.Running {
		err = dc.StartContainer(env.Container.Name)
		if err != nil {
			return env, err
		}
	}

	return GetEnvironment(dc, sc, envName)
}

func EnsureStopped(dc DockerClient, sc SystemClient, envName string) (Environment, error) {
	var env Environment

	envs, err := Environments(dc, sc)
	if err != nil {
		return env, err
	}
	env, ok := envs[envName]

	if !ok {
		return env, fmt.Errorf("Environment %s doesn't exist.", envName)
	}

	if env.Container != nil && env.Container.Running {
		err = dc.StopContainer(env.Container.Name)
		if err != nil {
			return env, err
		}
	}

	return GetEnvironment(dc, sc, envName)
}

func ResolveImage(dc DockerClient, io ImageOpts) (string, error) {
	var image string
	if len(io.Type) > 0 {
		baseImages, err := BaseImages(dc)
		if err != nil {
			return "", err
		}

		var matcher func(*BaseImageTag) bool
		if len(io.Version) > 0 {
			matcher = func(tag *BaseImageTag) bool {
				return tag.Name == io.Version
			}
		} else {
			matcher = func(tag *BaseImageTag) bool {
				return tag.Preferred
			}
		}
		for _, im := range baseImages {
			if io.Type == im.Name {
				for _, tag := range im.Tags {
					if matcher(tag) {
						image = fmt.Sprintf("%s/%s:%s", DOCKER_HUB_ORG, im.Name, tag.Name)
					}
				}
			}
		}
		if len(image) == 0 {
			return "", fmt.Errorf("No image found")
		}
	} else if len(io.Image) > 0 {
		image = io.Image
	}

	return image, nil
}

func BuildImage(dc DockerClient, bo BuildOpts, output *os.File) (string, error) {
	var err error
	logrus.Debugf("Figuring out which image to use")
	image, err := ResolveImage(dc, bo.Image)
	if err != nil {
		return "", err
	}

	logrus.Debugf("Using image: %s", image)
	err = EnsureImage(dc, image, bo.ForcePull, output)
	if err != nil {
		return "", err
	}

	now := time.Now()

	logrus.Debugf("Building image")
	dockerfileTmpl := `FROM {{ .Image }}

RUN (addgroup --gid {{ .Gid }} {{ .Username }} || /bin/true) && \
    adduser --uid {{ .Uid }} --gid {{ .Gid }} {{ .Username }} --gecos "" --disabled-password && \
    echo "{{ .Username }}   ALL=NOPASSWD: ALL" >> /etc/sudoers

{{ .TzSet }}

LABEL skeg.io/image/username={{ .Username }} \
      skeg.io/image/gid={{ .Gid }} \
      skeg.io/image/uid={{ .Uid }} \
      skeg.io/image/base={{ .Image }} \
      skeg.io/image/buildtime="{{ .Time }}"

`
	// TODO: make timezone setting work on other distributions
	var tzenv string
	if len(bo.TimeZone) > 0 {
		tzenv = fmt.Sprintf(`RUN echo "%s" > /etc/timezone && dpkg-reconfigure --frontend noninteractive tzdata`, bo.TimeZone)
	}

	dockerfileData := struct {
		Username, Image, Time, TzSet string
		Uid, Gid                     int
	}{
		bo.Username, image, now.Format(time.UnixDate), tzenv, bo.UID, bo.GID,
	}

	tmpl := template.Must(template.New("dockerfile").Parse(dockerfileTmpl))
	var dockerfileBytes bytes.Buffer

	err = tmpl.Execute(&dockerfileBytes, dockerfileData)
	if err != nil {
		return "", nil
	}

	imageName := fmt.Sprintf("%s-%s-%s", CONT_PREFIX, bo.Username, now.Format("20060102150405"))
	err = dc.BuildImage(imageName, dockerfileBytes.String(), output)

	if err != nil {
		return "", err
	}

	return imageName, nil
}

func UserImages(dc DockerClient, sc SystemClient, io ImageOpts) ([]UserImage, error) {
	images := make([]UserImage, 0)

	image, err := ResolveImage(dc, io)
	if err != nil {
		return images, err
	}

	labels := []string{
		fmt.Sprintf("skeg.io/image/base=%s", image),
		fmt.Sprintf("skeg.io/image/username=%s", sc.Username()),
	}
	dockerImages, err := dc.ListImagesWithLabels(labels)
	if err != nil {
		return images, err
	}

	for _, dockerImage := range dockerImages {
		tags := dockerImage.RepoTags
		sort.Strings(tags)

		images = append(images, UserImage{
			tags[0],
			tags[1:],
			dockerImage.Labels,
		})
	}

	sort.Sort(sort.Reverse(ByName(images)))

	return images, nil
}

func BaseImages(dc DockerClient) ([]*BaseImage, error) {

	images := make([]*BaseImage, 0)

	dockerImages, err := dc.ListImages()
	if err != nil {
		return images, err
	}

	tagToImage := make(map[string]docker.APIImages)
	for _, im := range dockerImages {
		for _, tag := range im.RepoTags {
			tagToImage[tag] = im
		}
	}

	// TODO: get this information from somewhere else.  API?
	var baseImages = []*BaseImage{
		{
			"go",
			"Golang Image",
			[]*BaseImageTag{
				{"1.5", false, false},
				{"1.6", false, true},
			},
		},
		{
			"clojure",
			"Clojure image",
			[]*BaseImageTag{
				{"java7", false, true},
			},
		},
		{
			"python",
			"Python base image",
			[]*BaseImageTag{
				{"both", false, true},
				{"2.7", false, false},
				{"3.4", false, false},
			},
		},
	}

	for _, bimage := range baseImages {
		for _, btag := range bimage.Tags {
			imageTag := fmt.Sprintf("%s/%s:%s", dockerOrg, bimage.Name, btag.Name)
			if _, ok := tagToImage[imageTag]; ok {
				btag.Pulled = true
			}
		}
	}

	return baseImages, nil
}

func GetEnvironment(dc DockerClient, sc SystemClient, name string) (Environment, error) {
	envs, err := Environments(dc, sc)
	if err != nil {
		return Environment{}, err
	}

	env, ok := envs[name]

	if !ok {
		return Environment{}, fmt.Errorf("%s environment not found", name)
	}

	return env, nil
}

func ConnectEnvironment(dc DockerClient, sc SystemClient, name string, extra []string) error {
	env, err := EnsureRunning(dc, sc, name)
	if err != nil {
		return err
	}

	// TODO: create container
	if env.Container == nil {
		return errors.New("No container found")
	}

	var host string
	if env_endpoint := os.Getenv("DOCKER_HOST"); len(env_endpoint) > 0 {
		re, err := regexp.Compile(`(tcp://)?([^:]+)(:\d+)?`)
		if err != nil {
			return err
		}

		res := re.FindAllStringSubmatch(env_endpoint, -1)
		host = res[0][2]
	} else {
		host = "localhost"
	}

	var sshPort string
	for _, port := range env.Container.Ports {
		if port.ContainerPort == 22 {
			sshPort = fmt.Sprintf("%d", port.HostPort)
		}
	}

	if len(sshPort) == 0 {
		return errors.New("Running container doesn't have ssh running")
	}

	key, err := sc.EnsureSSHKey()
	if err != nil {
		return err
	}

	opts := []string{
		host,
		"-l", sc.Username(),
		"-p", sshPort,
		"-i", key.privatePath,
		"-o", "UserKnownHostsFile /dev/null",
		"-o", "StrictHostKeyChecking no",
	}

	cmd := exec.Command(
		"ssh", append(opts, extra...)...,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func Environments(dc DockerClient, sc SystemClient) (map[string]Environment, error) {
	envs := make(map[string]Environment)

	dockerContainers, err := dc.ListContainers()
	if err != nil {
		return envs, err
	}

	containersByName := make(map[string]*Container)
	for _, cont := range dockerContainers {
		name := strings.TrimPrefix(cont.Names[0], "/")
		ports := make([]Port, 0)
		for _, cPort := range cont.Ports {
			ports = append(ports, Port{
				HostIp:        cPort.IP,
				HostPort:      cPort.PublicPort,
				ContainerPort: cPort.PrivatePort,
				Type:          cPort.Type,
			})
		}
		containersByName[name] = &Container{
			Name:    name,
			Image:   cont.Image,
			Running: strings.Contains(cont.Status, "Up"),
			Ports:   ports,
			Labels:  cont.Labels,
		}
	}

	files, err := sc.EnvironmentDirs()
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		contName := fmt.Sprintf("%s_%s", CONT_PREFIX, file)
		newEnv := Environment{
			Name:      file,
			Container: containersByName[contName],
		}

		if cont, ok := containersByName[contName]; ok {
			newEnv.Type, ok = cont.Labels["skeg.io/image/base"]
			if !ok {
				newEnv.Type = "unknown"
			}
		}

		envs[file] = newEnv
	}

	return envs, nil
}

func EnsureImage(dc DockerClient, image string, forcePull bool, output *os.File) error {
	_, tag := dc.ParseRepositoryTag(image)
	if len(tag) == 0 {
		image = fmt.Sprintf("%s:latest", image)
	}

	if !forcePull {
		dockerImages, err := dc.ListImages()
		if err != nil {
			return err
		}

		for _, im := range dockerImages {
			for _, tag := range im.RepoTags {
				if tag == image {
					return nil
				}
			}
		}
	}

	logrus.Debugf("Pulling image: %s", image)
	return dc.PullImage(image, output)
}
