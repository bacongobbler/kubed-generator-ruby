package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/bacongobbler/kubed-generator-sdk-go/manifest"
	"github.com/bacongobbler/kubed-generator-sdk-go/pack"
)

const (
	environmentEnvVar = "KUBED_ENV"
	globalUsage       = `Generates boilerplate code that is necessary to write a ruby app.
`
	dockerfile = `FROM ruby:2.4

# throw errors if Gemfile has been modified since Gemfile.lock
RUN bundle config --global frozen 1

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app

COPY Gemfile /usr/src/app/
COPY Gemfile.lock /usr/src/app/
RUN bundle install

COPY . /usr/src/app

ENV PORT 8080
EXPOSE 8080
CMD ["ruby", "app.rb"]
`
	dockerIgnore = `tmp/
`
	appRb = `require 'sinatra'

set :bind, '0.0.0.0'
set :port, ENV['PORT']

get '/' do
  'Hello World, I\'m a Ruby Sinatra app!'
end
`
	gemfile = `source 'https://rubygems.org'

gem 'sinatra'
`
	gemfileLock = `GEM
  remote: https://rubygems.org/
  specs:
    mustermann (1.0.2)
    rack (2.0.5)
    rack-protection (2.0.3)
	  rack
    sinatra (2.0.3)
	  mustermann (~> 1.0)
	  rack (~> 2.0)
	  rack-protection (= 2.0.3)
	  tilt (~> 2.0)
    tilt (2.0.8)

PLATFORMS
  ruby

DEPENDENCIES
  sinatra

BUNDLED WITH
  1.16.2
`
	deploymentTemplate = `kind: Deployment
apiVersion: apps/v1
metadata:
  name: {{ template "{% .AppName %}.{% .Name %}.name" . }}
  labels:
    kubed: {{ template "{% .AppName %}.name" . }}
		component: {% .Name %}
		generator: ruby
spec:
  selector:
    matchLabels:
      kubed: {{ template "{% .AppName %}.name" . }}
      component: {% .Name %}
  replicas: {{ default .Values.{% .Name %}.replicaCount 1 }}
  template:
    metadata:
      annotations:
        buildID: {{ .Values.buildID }}
      labels:
        kubed: {{ template "{% .AppName %}.name" . }}
        component: {% .Name %}
    spec:
      containers:
        - name: {% .Name %}
          image: "{{ .Values.{% .Name %}.image.repository }}:{{ .Values.{% .Name %}.image.tag }}"
          imagePullPolicy: {{ default .Values.{% .Name %}.image.pullPolicy "IfNotPresent" }}
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
`
	serviceTemplate = `kind: Service
apiVersion: v1
metadata:
  name: {{ template "{% .AppName %}.{% .Name %}.name" . }}
  labels:
    kubed: {{ template "{% .AppName %}.name" . }}
		component: {% .Name %}
		generator: ruby
spec:
  selector:
    kubed: {{ template "{% .AppName %}.name" . }}
    component: {% .Name %}
  ports:
    - port: 80
      targetPort: http
      protocol: TCP
      name: http
`
	helperTemplate = `
{{- define "{% .AppName %}.{% .Name %}.name" -}}
{{- printf "%s-{% .Name %}" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
`
	valuesTemplate = `
{% .Name %}:
  image: {}
`
)

var flagDebug bool

type generateCmd struct {
	stdout         io.Writer
	name           string
	repositoryName string
}

func newRootCmd(stdout io.Writer, stdin io.Reader, stderr io.Writer) *cobra.Command {
	c := generateCmd{
		stdout: stdout,
	}

	cmd := &cobra.Command{
		Use:          "generator-ruby <name>",
		Short:        "generates ruby applications",
		Long:         globalUsage,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if flagDebug {
				log.SetLevel(log.DebugLevel)
			}
			c.name = args[0]
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run()
		},
	}

	pf := cmd.PersistentFlags()
	pf.BoolVar(&flagDebug, "debug", false, "enable verbose output")

	return cmd
}

func (c *generateCmd) run() error {
	var config manifest.Manifest
	tomlFilepath := filepath.Join("config", "kubed.toml")
	if _, err := toml.DecodeFile(tomlFilepath, &config); err != nil {
		return err
	}
	appConfig, found := config.Environments[defaultEnvironment()]
	if !found {
		return fmt.Errorf("Environment %v not found in %s", defaultEnvironment(), tomlFilepath)
	}

	deploymentFile, err := os.Create(filepath.Join("charts", appConfig.Name, "templates", fmt.Sprintf("%s-deployment.yaml", c.name)))
	if err != nil {
		return err
	}
	defer deploymentFile.Close()
	serviceFile, err := os.Create(filepath.Join("charts", appConfig.Name, "templates", fmt.Sprintf("%s-service.yaml", c.name)))
	if err != nil {
		return err
	}
	defer serviceFile.Close()
	valuesFile, err := os.OpenFile(filepath.Join("charts", appConfig.Name, "values.yaml"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer valuesFile.Close()
	helpersFile, err := os.OpenFile(filepath.Join("charts", appConfig.Name, "templates", "_helpers.tpl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer helpersFile.Close()

	// scaffold helm chart
	values := struct {
		AppName       string
		Name          string
		GeneratorName string
	}{
		AppName:       appConfig.Name,
		Name:          c.name,
		GeneratorName: "ruby",
	}
	dt := template.Must(template.New("deployment").Delims("{%", "%}").Parse(deploymentTemplate))
	if err := dt.Execute(deploymentFile, values); err != nil {
		return err
	}

	st := template.Must(template.New("service").Delims("{%", "%}").Parse(serviceTemplate))
	if err := st.Execute(serviceFile, values); err != nil {
		return err
	}

	vt := template.Must(template.New("values").Delims("{%", "%}").Parse(valuesTemplate))
	if err := vt.Execute(valuesFile, values); err != nil {
		return err
	}

	ht := template.Must(template.New("helpers").Delims("{%", "%}").Parse(helperTemplate))
	if err := ht.Execute(helpersFile, values); err != nil {
		return err
	}

	// scaffold business logic
	if _, err := os.Stat(c.name); os.IsNotExist(err) {
		if err := os.Mkdir(c.name, 0777); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("there was an error checking if %s exists: %v", c.name, err)
	}

	p := &pack.Pack{
		Files: map[string]io.ReadCloser{
			"Dockerfile":    ioutil.NopCloser(bytes.NewBufferString(dockerfile)),
			".dockerignore": ioutil.NopCloser(bytes.NewBufferString(dockerIgnore)),
			"app.rb":        ioutil.NopCloser(bytes.NewBufferString(appRb)),
			"Gemfile":       ioutil.NopCloser(bytes.NewBufferString(gemfile)),
			"Gemfile.lock":  ioutil.NopCloser(bytes.NewBufferString(gemfileLock)),
		},
	}

	if err := p.SaveDir(c.name); err != nil {
		return err
	}

	// Each pack makes the assumption that they're listening on port 8080
	addRoute(filepath.Join("config", "routes"), fmt.Sprintf("/%s/\t%s\t8080", c.name, c.name))

	fmt.Fprintln(c.stdout, "--> Ready to sail")
	return nil
}

// addRoute adds a new route to fpath. It appends the route
// above the default route so that it takes higher priority
// in the list than the static files, but lower priority than
// other routes higher up in the list.
func addRoute(fpath, route string) error {
	const defaultRoute = "/\tstatic\t"
	b, err := ioutil.ReadFile(fpath)
	if err != nil {
		return err
	}
	content := string(b)
	fileContent := ""
	n, defaultRouteExists := containsDefaultRoute(content)
	if defaultRouteExists {
		for i, line := range strings.Split(content, "\n") {
			if i == n {
				fileContent += route + "\n"
			}
			fileContent += line + "\n"
		}
	} else {
		fileContent = content
		if !strings.HasSuffix(fileContent, "\n") && fileContent != "" {
			fileContent += "\n"
		}
		fileContent += route + "\n"
	}
	return ioutil.WriteFile(fpath, []byte(fileContent), 0644)
}

// containsDefaultRoute determines if the content contains a line starting with
//
// / static 8080 /
//
// if it does, it returns the line number (0-indexed) where the first instance
// of that route is found.
func containsDefaultRoute(content string) (int, bool) {
	for i, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			if fields[0] == "/" && fields[1] == "static" &&
				fields[2] == "8080" && fields[3] == "/" {
				return i, true
			}
		}
	}
	return 0, false
}

func main() {
	cmd := newRootCmd(os.Stdout, os.Stdin, os.Stderr)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func defaultEnvironment() string {
	env := os.Getenv(environmentEnvVar)
	if env == "" {
		env = manifest.DefaultEnvironmentName
	}
	return env
}
