package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kardianos/osext"
	"github.com/mingxi/service"
	"golang.org/x/sys/windows/registry"
)

// Config is the runner app config structure.
type Config struct {
	Name, DisplayName, Description string

	Dir  string
	Exec string
	Args []string
	Env  []string

	Stderr, Stdout string
}

var logger service.Logger

type program struct {
	exit    chan struct{}
	service service.Service

	*Config

	cmd *exec.Cmd
}

func (p *program) Start(s service.Service, args ...string) error {
	p.setEnvs()
	// Look for exec.
	// Verify home directory.
	if p.Dir != "" {
		fi, err := os.Stat(p.Dir)
		if err != nil {
			return err
		} else if fi.IsDir() {
			os.Chdir(p.Dir)
		}
	} else {
		dir, _, err := getExecPath()
		if err != nil {
			return err
		} else {
			os.Chdir(dir)
		}
	}
	fullExec, err := exec.LookPath(p.Exec)
	if err != nil {
		return fmt.Errorf("Failed to find executable %q: %v", p.Exec, err)
	}
	p.cmd = exec.Command(fullExec, p.Args...)
	p.cmd.Env = append(os.Environ(), p.Env...)
	go p.run()
	return nil
}

func (p *program) setEnvs() {
	for _, env := range p.Env {
		kv := strings.SplitN(env, "=", 2)
		if len(kv) == 2 {
			if strings.TrimSpace(strings.ToLower(kv[0])) == "path" {
				pathEnv := os.ExpandEnv(fmt.Sprintf("%s;$PATH", kv[1]))
				os.Setenv("PATH", pathEnv)
			} else {
				os.Setenv(kv[0], kv[1])
			}
		}
	}
}
func (p *program) run() {
	logger.Info("Starting ", p.DisplayName)
	defer func() {
		if service.Interactive() {
			p.Stop(p.service)
		} else {
			p.service.Stop()
		}
	}()

	if p.Stderr != "" {
		f, err := os.OpenFile(p.Stderr, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0777)
		if err != nil {
			logger.Warningf("Failed to open std err %q: %v", p.Stderr, err)
			return
		}
		defer f.Close()
		p.cmd.Stderr = f
	}
	if p.Stdout != "" {
		f, err := os.OpenFile(p.Stdout, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0777)
		if err != nil {
			logger.Warningf("Failed to open std out %q: %v", p.Stdout, err)
			return
		}
		defer f.Close()
		p.cmd.Stdout = f
	}
	err := p.cmd.Run()
	if err != nil {
		logger.Warningf("Error running: %v", err)
	}
	return
}
func (p *program) Stop(s service.Service) error {
	close(p.exit)
	logger.Info("Stopping ", p.DisplayName)
	if service.Interactive() {
		os.Exit(0)
	} else {
		p.cmd.Process.Kill()
	}
	return nil
}

func getExecPath() (string, string, error) {
	fullexecpath, err := osext.Executable()
	if err != nil {
		return "", "", err
	}

	dir, execname := filepath.Split(fullexecpath)
	return dir, execname, nil
}

func getConfigPath() (string, error) {
	dir, execname, err := getExecPath()
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(execname)
	name := execname[:len(execname)-len(ext)]
	return filepath.Join(dir, name+".json"), nil
}

func getConfig() (*Config, error) {
	configPath, err := getConfigPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(configPath)
	if err != nil {
		_, execname, err := getExecPath()
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, fmt.Sprintf("SOFTWARE\\%s", execname), registry.READ)
		if err == nil {
			defer key.Close()
			data, _, err := key.GetBinaryValue("config")
			if err == nil {
				conf := &Config{}
				err := json.Unmarshal(data, &conf)
				if err != nil {
					return nil, err
				}
				return conf, nil
			}
			return nil, err
		}
		return nil, err
	}
	defer f.Close()
	conf := &Config{}
	data, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(data, &conf)
	if err != nil {
		return nil, err
	}
	return conf, nil
}

func initConfig() {
	config := &Config{Name: "srv", DisplayName: "srv", Description: "Service", Exec: "main.exe"}
	data, err := json.Marshal(&config)
	if err == nil {
		cfp, err := getConfigPath()
		if err == nil {
			ioutil.WriteFile(cfp, data, 0755)
		}
	}
}

func createConfig(config *Config) {
	_, execname, err := getExecPath()
	if err == nil {
		key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, fmt.Sprintf("SOFTWARE\\%s", execname), registry.ALL_ACCESS)
		if err == nil {
			defer key.Close()
			data, err := json.Marshal(&config)
			if err == nil {
				if err == nil {
					key.SetBinaryValue("config", data)
				}
			}
		}
	}
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("wsw -a init/start/stop/restart/install/uninstall")
}

func main() {
	svcAction := flag.String("a", "", "Control the system service.")
	flag.Parse()
	if len(*svcAction) != 0 {
		if *svcAction == "init" {
			initConfig()
			return
		}
	}
	config, err := getConfig()
	if err != nil {
		log.Fatal(err)
	}
	createConfig(config)
	svcConfig := &service.Config{
		Name:        config.Name,
		DisplayName: config.DisplayName,
		Description: config.Description,
	}

	prg := &program{
		exit: make(chan struct{}),

		Config: config,
	}
	s, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatal(err)
	}
	prg.service = s

	errs := make(chan error, 5)
	logger, err = s.Logger(errs)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			err := <-errs
			if err != nil {
				log.Print(err)
			}
		}
	}()
	handleAction(s, *svcAction)
}

func handleAction(s service.Service, action string) {
	if len(action) != 0 {
		err := service.Control(s, action)
		if err != nil {
			log.Printf("Valid actions: %q\n", service.ControlAction)
			log.Fatal(err)
		}
	} else {
		err := s.Run()
		if err != nil {
			log.Fatal(err)
		}
	}
}
