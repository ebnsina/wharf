package detect

import (
	"strconv"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// PythonDetector recognises Python services, primarily ASGI/WSGI apps.
type PythonDetector struct{}

func (PythonDetector) Name() string { return "python" }

func (d PythonDetector) Detect(dir string) (manifest.Service, bool) {
	if !exists(dir, "pyproject.toml") && !exists(dir, "requirements.txt") {
		return manifest.Service{}, false
	}

	svc := manifest.Service{
		Name:  serviceName(dir),
		Path:  dir,
		Kind:  manifest.KindService,
		Stack: manifest.StackPython,
	}

	svc.Config = findConfigSources(dir)
	if len(svc.Config) > 0 {
		res := probe(dir, svc.Config[0])
		svc.Berth = res.Port
		svc.Config[0].PortKey = res.PortKey
		svc.Config[0].PortTemplate = res.PortTemplate
		svc.Needs = res.Needs
	}

	svc.Processes = []manifest.Process{{
		Name:    "api",
		Cmd:     d.serveCmd(dir, svc.Berth),
		Primary: true,
	}}

	svc.Lifecycle = manifest.Lifecycle{
		Install: d.installCmd(dir),
		Migrate: d.migrateCmd(dir),
		Seed:    d.seedCmd(dir),
		Test:    "pytest",
	}

	if compose := composeFile(dir); compose != "" {
		for i := range svc.Needs {
			svc.Needs[i].Compose = compose
		}
	}
	if svc.Berth > 0 {
		svc.Health = &manifest.Health{Type: "tcp", TimeoutSeconds: 45}
	}
	return svc, true
}

// asgiApp finds the module:app target uvicorn should serve.
func (d PythonDetector) asgiApp(dir string) string {
	for _, candidate := range []string{"app.main:app", "main:app", "src.main:app"} {
		path := strings.ReplaceAll(strings.Split(candidate, ":")[0], ".", "/") + ".py"
		if exists(dir, path) {
			return candidate
		}
	}
	return ""
}

func (d PythonDetector) serveCmd(dir string, berth int) string {
	if app := d.asgiApp(dir); app != "" {
		cmd := "uvicorn " + app + " --reload"
		if berth > 0 {
			cmd += " --port " + strconv.Itoa(berth)
		}
		return cmd
	}
	if exists(dir, "manage.py") {
		return "python manage.py runserver"
	}
	return "python main.py"
}

func (d PythonDetector) installCmd(dir string) string {
	if exists(dir, "requirements.txt") {
		return "pip install -r requirements.txt"
	}
	if exists(dir, "uv.lock") {
		return "uv sync"
	}
	if exists(dir, "poetry.lock") {
		return "poetry install"
	}
	return "pip install -e ."
}

func (d PythonDetector) migrateCmd(dir string) string {
	if exists(dir, "alembic.ini") {
		return "alembic upgrade head"
	}
	if exists(dir, "manage.py") {
		return "python manage.py migrate"
	}
	return ""
}

func (d PythonDetector) seedCmd(dir string) string {
	for _, candidate := range []string{"scripts/seed_db.py", "scripts/seed.py", "seed.py"} {
		if exists(dir, candidate) {
			return "python " + candidate
		}
	}
	if exists(dir, "manage.py") {
		return "python manage.py loaddata"
	}
	return ""
}
