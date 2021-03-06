package main

import (
	"fmt"
	"os"
)

type CreateCommand struct {
	Type       string   `short:"t" long:"type" description:"Type of environment."`
	Version    string   `short:"v" long:"version" description:"Version of environment type."`
	Image      string   `short:"i" long:"image" description:"Image to use for creating environment."`
	Directory  string   `short:"d" long:"directory" description:"Directory to mount inside (defaults to $PWD)."`
	Ports      []string `short:"p" long:"port" description:"Ports to expose (similar to docker -p)."`
	Volumes    []string `long:"volume" description:"Volume to mount (similar to docker -v)."`
	ForceBuild bool     `long:"force-build" description:"Force building of new user image."`
	ForcePull  bool     `long:"force-pull" description:"Force pulling base image."`
	TimeZone   string   `long:"tz" description:"Time zone for container, specify like 'America/Los_Angeles'.  Defaults to UTC."`
	Args       struct {
		Name string `description:"Name of environment."`
	} `positional-args:"yes" required:"yes"`
}

func (ccommand *CreateCommand) toCreateOpts(sc SystemClient, workingDir string) CreateOpts {
	var projectDir string
	if len(ccommand.Directory) > 0 {
		projectDir = ccommand.Directory
	} else {
		projectDir = workingDir
	}
	return CreateOpts{
		Name:       ccommand.Args.Name,
		ProjectDir: projectDir,
		Ports:      ccommand.Ports,
		Volumes:    ccommand.Volumes,
		ForceBuild: ccommand.ForceBuild || ccommand.ForcePull,
		Build: BuildOpts{
			Image: ImageOpts{
				Type:    ccommand.Type,
				Version: ccommand.Version,
				Image:   ccommand.Image,
			},
			TimeZone:  ccommand.TimeZone,
			ForcePull: ccommand.ForcePull,
			Username:  sc.Username(),
			UID:       sc.UID(),
			GID:       sc.GID(),
		},
	}
}

var createCommand CreateCommand

func (x *CreateCommand) Execute(args []string) error {
	dc, err := NewDockerClient(globalOptions.toConnectOpts())
	if err != nil {
		return err
	}

	sc, err := NewSystemClient()
	if err != nil {
		return err
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return err
	}

	return CreateNewEnvironment(dc, sc, createCommand.toCreateOpts(sc, workingDir), os.Stdout)
}

func init() {
	_, err := parser.AddCommand("create",
		"Create an environment.",
		"",
		&createCommand)

	if err != nil {
		fmt.Println(err)
	}
}
