package agent

import (
	"context"
	"testing"

	"backupmanagementcenter/internal/model"
)

func TestProbe_DefaultToolNames(t *testing.T) {
	p := NewProberWithProbeFn(func(ctx context.Context, toolName string) (path, version string) {
		return "/usr/bin/" + toolName, toolName + "-1.0.0"
	})

	results := p.Probe(context.Background())
	if len(results) != len(toolNames) {
		t.Fatalf("expected %d tools, got %d", len(toolNames), len(results))
	}
	for _, tool := range results {
		if tool.Path != "/usr/bin/"+tool.Name {
			t.Errorf("tool %s: expected path /usr/bin/%s, got %s", tool.Name, tool.Name, tool.Path)
		}
		if tool.Version != tool.Name+"-1.0.0" {
			t.Errorf("tool %s: expected version %s-1.0.0, got %s", tool.Name, tool.Name, tool.Version)
		}
	}
}

func TestProbe_FakeTools(t *testing.T) {
	finders := map[string]struct {
		path    string
		version string
	}{
		"restic":   {"/usr/local/bin/restic", "restic 0.17.3"},
		"rclone":   {"/usr/local/bin/rclone", "rclone v1.68.0"},
		"psql":     {"/usr/bin/psql", "psql (PostgreSQL) 16.4"},
		"sqlite3":  {"/usr/bin/sqlite3", "3.44.0"},
		"mongodump": {"/usr/bin/mongodump", "mongodump version v7.0.12"},
		"pg_dump":   {"/usr/bin/pg_dump", "pg_dump (PostgreSQL) 16.4"},
		"mysqldump": {"/usr/bin/mysqldump", "mysqldump  Ver 8.4.3"},
		"mysql":     {"/usr/bin/mysql", "mysql  Ver 8.4.3"},
		"pg_restore": {"/usr/bin/pg_restore", "pg_restore (PostgreSQL) 16.4"},
		"mongorestore": {"/usr/bin/mongorestore", "mongorestore version v7.0.12"},
	}

	p := NewProberWithProbeFn(func(ctx context.Context, toolName string) (path, version string) {
		if f, ok := finders[toolName]; ok {
			return f.path, f.version
		}
		return "", ""
	})

	results := p.Probe(context.Background())
	if len(results) != len(toolNames) {
		t.Fatalf("expected %d tools, got %d", len(toolNames), len(results))
	}

	for _, tool := range results {
		if !toolFound(finders, tool.Name) {
			continue
		}
		expected := finders[tool.Name]
		if tool.Path != expected.path {
			t.Errorf("tool %s: expected path %s, got %s", tool.Name, expected.path, tool.Path)
		}
		if tool.Version != expected.version {
			t.Errorf("tool %s: expected version %s, got %s", tool.Name, expected.version, tool.Version)
		}
	}
}

func TestProbe_MissingTool(t *testing.T) {
	p := NewProberWithProbeFn(func(ctx context.Context, toolName string) (path, version string) {
		return "", ""
	})

	results := p.Probe(context.Background())
	for _, tool := range results {
		if tool.Path != "" || tool.Version != "" {
			t.Errorf("tool %s: expected empty path and version when missing, got path=%q version=%q", tool.Name, tool.Path, tool.Version)
		}
	}
}

func TestProbe_CachedTools(t *testing.T) {
	calls := 0
	p := NewProberWithProbeFn(func(ctx context.Context, toolName string) (path, version string) {
		calls++
		return "/bin/" + toolName, "1.0"
	})

	_ = p.Probe(context.Background())
	if calls != len(toolNames) {
		t.Fatalf("expected %d probe calls, got %d", len(toolNames), calls)
	}

	cached := p.GetCached()
	if len(cached) != len(toolNames) {
		t.Fatalf("expected %d cached tools, got %d", len(toolNames), len(cached))
	}
	for name, info := range cached {
		if info.Path != "/bin/"+name {
			t.Errorf("cached tool %s: expected path /bin/%s, got %s", name, name, info.Path)
		}
	}
}

func TestProbe_ResultModels(t *testing.T) {
	p := NewProberWithProbeFn(func(ctx context.Context, toolName string) (path, version string) {
		return "/test/path", "test-version"
	})

	results := p.Probe(context.Background())
	for _, r := range results {
		// Verify the model fields are present and usable
		ti := model.ToolInfo{
			Name:    r.Name,
			Path:    r.Path,
			Version: r.Version,
		}
		if ti.Name != r.Name {
			t.Errorf("tool info name mismatch for %s", r.Name)
		}
		if ti.Path != r.Path {
			t.Errorf("tool info path mismatch for %s", r.Name)
		}
		if ti.Version != r.Version {
			t.Errorf("tool info version mismatch for %s", r.Name)
		}
	}
}

func toolFound(finders map[string]struct{ path, version string }, name string) bool {
	_, ok := finders[name]
	return ok
}