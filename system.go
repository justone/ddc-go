package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

type SystemClient interface {
	EnvironmentDirs() ([]string, error)
	EnsureEnvironmentDir(envName string, keys SSHKey) (string, error)
	RemoveEnvironmentDir(envName string) error
	EnsureSSHKey() (SSHKey, error)
	Username() string
	UID() int
	GID() int
}

type RealSystemClient struct {
	user      string
	uid       int
	gid       int
	baseDir   string
	envRegexp *regexp.Regexp
}

type SSHKey struct {
	privatePath string
	publicPath  string
}

func (rsc *RealSystemClient) EnvironmentDirs() ([]string, error) {
	files, err := ioutil.ReadDir(rsc.baseDir)
	if err != nil {
		return nil, err
	}

	dirs := make([]string, 0)
	for _, file := range files {
		if file.IsDir() {
			dirs = append(dirs, file.Name())
		}
	}

	return dirs, nil
}

func (rsc *RealSystemClient) Username() string {
	return rsc.user
}

func (rsc *RealSystemClient) UID() int {
	return rsc.uid
}

func (rsc *RealSystemClient) GID() int {
	return rsc.gid
}

func (rsc *RealSystemClient) EnsureEnvironmentDir(envName string, keys SSHKey) (string, error) {

	envPath := filepath.Join(rsc.baseDir, envName)
	err := os.MkdirAll(envPath, 0755)
	if err != nil {
		return "", err
	}

	sshPath := filepath.Join(envPath, ".ssh")
	err = os.MkdirAll(sshPath, 0700)
	if err != nil {
		return "", err
	}

	akPath := filepath.Join(sshPath, "authorized_keys")
	data, err := ioutil.ReadFile(keys.publicPath)
	if err != nil {
		return "", err
	}
	err = ioutil.WriteFile(akPath, data, 0700)
	if err != nil {
		return "", err
	}

	return envPath, nil
}

func (rsc *RealSystemClient) RemoveEnvironmentDir(envName string) error {

	envPath := filepath.Join(rsc.baseDir, envName)
	err := os.RemoveAll(envPath)
	if err != nil {
		return err
	}

	return nil
}

func (rsc *RealSystemClient) EnsureSSHKey() (SSHKey, error) {
	privPath := filepath.Join(rsc.baseDir, "skeg_key")
	pubPath := filepath.Join(rsc.baseDir, "skeg_key.pub")

	if _, err := os.Stat(privPath); os.IsNotExist(err) {

		cmd := exec.Command("ssh-keygen", "-q", "-t", "rsa", "-N", "", "-C", "skeg key", "-f", privPath)
		err := cmd.Run()
		if err != nil {
			return SSHKey{}, err
		}
	}

	return SSHKey{privPath, pubPath}, nil
}

func NewSystemClient() (*RealSystemClient, error) {

	var home string
	if home = os.Getenv("HOME"); len(home) == 0 {
		return nil, fmt.Errorf("$HOME environment variable not found")
	}

	return NewSystemClientWithBase(filepath.Join(home, "envs"))
}

func NewSystemClientWithBase(baseDir string) (*RealSystemClient, error) {

	var user string

	if user = os.Getenv("USER"); len(user) == 0 {
		return nil, fmt.Errorf("$USER environment variable not found")
	}

	var uid int
	if env_endpoint := os.Getenv("DOCKER_MACHINE_NAME"); len(env_endpoint) > 0 {
		uid = 1000
	} else {
		uid = os.Getuid()
	}

	systemClient := RealSystemClient{
		user:      user,
		uid:       uid,
		gid:       os.Getgid(),
		baseDir:   baseDir,
		envRegexp: regexp.MustCompile(fmt.Sprintf("%s/(.*)dev", user)),
	}

	err := os.MkdirAll(baseDir, 0700)
	if err != nil {
		return nil, err
	}

	return &systemClient, nil
}
