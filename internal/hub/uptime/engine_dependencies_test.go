//go:build testing

package uptime

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// takeNamed returns and clears the notices as "name:status".
func (env *testEnv) takeNamed() []string {
	named := []string{}
	for _, transition := range env.takeTransitions() {
		named = append(named, transition.Name+":"+transition.Status)
	}
	return named
}

func TestDependencySuppression(t *testing.T) {
	type step struct {
		monitor string // parent, child, grand
		action  string // ok, fail, maint+, maint-
		notices []string
		// suppressed is the child's suppressedBy after the step.
		suppressed string
	}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"parent down suppresses the child", []step{
			{"parent", "ok", nil, ""}, {"child", "ok", nil, ""},
			{"parent", "fail", []string{"Router:down"}, "Router"},
			{"child", "fail", nil, "Router"},
			{"child", "fail", nil, "Router"},
		}},
		{"parent recovers with the child still down notifies once", []step{
			{"parent", "ok", nil, ""}, {"child", "ok", nil, ""},
			{"parent", "fail", []string{"Router:down"}, "Router"},
			{"child", "fail", nil, "Router"},
			{"parent", "ok", []string{"Router:up", "Web:down"}, ""},
			{"child", "fail", nil, ""},
			{"child", "ok", []string{"Web:up"}, ""},
		}},
		{"child recovered while suppressed is not notified", []step{
			{"parent", "ok", nil, ""}, {"child", "ok", nil, ""},
			{"parent", "fail", []string{"Router:down"}, "Router"},
			{"child", "fail", nil, "Router"},
			{"child", "ok", nil, "Router"},
			{"parent", "ok", []string{"Router:up"}, ""},
		}},
		{"child already down notifies its recovery after the parent", []step{
			{"parent", "ok", nil, ""}, {"child", "ok", nil, ""},
			{"child", "fail", []string{"Web:down"}, ""},
			{"parent", "fail", []string{"Router:down"}, "Router"},
			{"child", "ok", nil, "Router"},
			{"parent", "ok", []string{"Router:up", "Web:up"}, ""},
		}},
		{"chain suppresses each level behind a down parent", []step{
			{"parent", "ok", nil, ""}, {"child", "ok", nil, ""}, {"grand", "ok", nil, ""},
			{"parent", "fail", []string{"Router:down"}, "Router"},
			{"child", "fail", nil, "Router"},
			{"grand", "fail", nil, "Router"},
			// The router recovers: the switch is still down, so the host stays suppressed.
			{"parent", "ok", []string{"Router:up", "Web:down"}, ""},
			{"child", "ok", []string{"Web:up", "Host:down"}, ""},
		}},
		{"parent maintenance is not down", []step{
			{"parent", "ok", nil, ""}, {"child", "ok", nil, ""},
			{"parent", "fail", []string{"Router:down"}, "Router"},
			{"child", "fail", nil, "Router"},
			{"parent", "maint+", []string{"Web:down"}, ""},
		}},
		{"child maintenance and a down parent", []step{
			{"parent", "ok", nil, ""}, {"child", "ok", nil, ""},
			{"child", "maint+", nil, ""},
			{"parent", "fail", []string{"Router:down"}, "Router"},
			{"child", "fail", nil, "Router"},
			{"parent", "ok", []string{"Router:up"}, ""},
			{"child", "maint-", []string{"Web:down"}, ""},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			parent := env.createMonitor(map[string]any{"name": "Router"})
			child := env.createMonitor(map[string]any{"name": "Web", "dependsOn": []string{parent.Id}})
			grand := env.createMonitor(map[string]any{"name": "Host", "dependsOn": []string{child.Id}})
			ids := map[string]string{"parent": parent.Id, "child": child.Id, "grand": grand.Id}
			for i, s := range tc.steps {
				id := ids[s.monitor]
				switch s.action {
				case "ok":
					env.check(id, true, "")
				case "fail":
					env.check(id, false, "timeout")
				case "maint+", "maint-":
					env.setMaintenance(id, s.action == "maint+")
					env.engine.Tick(env.advance(time.Second))
				}
				notices := s.notices
				if notices == nil {
					notices = []string{}
				}
				assert.Equal(t, notices, env.takeNamed(), "step %d (%s %s) notices", i, s.monitor, s.action)
				assert.Equal(t, s.suppressed, env.record(child.Id).GetString("suppressedBy"), "step %d (%s %s) suppressedBy", i, s.monitor, s.action)
				assert.Equal(t, s.suppressed != "", env.engine.Suppressed(child.Id), "step %d", i)
			}
		})
	}
}

func TestDependencyDisplayAndSegments(t *testing.T) {
	env := newTestEnv(t)
	parent := env.createMonitor(map[string]any{"name": "Router"})
	child := env.createMonitor(map[string]any{"name": "Web", "dependsOn": []string{parent.Id}})
	env.check(parent.Id, true, "")
	env.check(child.Id, true, "")
	env.check(parent.Id, false, "timeout")
	env.check(child.Id, false, "timeout")
	// The child keeps its own status and downtime.
	assert.Equal(t, StatusDown, env.record(child.Id).GetString("status"))
	segments := env.segments(child.Id)
	require.NotEmpty(t, segments)
	assert.Equal(t, StatusDown, segments[len(segments)-1].Status)
	assert.Equal(t, []string{"Web", "Router"}, env.engine.DownDependencies([]string{child.Id, parent.Id, "missing"}))
}

func TestDependencyConfigChanges(t *testing.T) {
	env := newTestEnv(t)
	parent := env.createMonitor(map[string]any{"name": "Router"})
	other := env.createMonitor(map[string]any{"name": "Switch"})
	child := env.createMonitor(map[string]any{"name": "Web", "dependsOn": []string{parent.Id, other.Id}})
	env.check(parent.Id, true, "")
	env.check(other.Id, true, "")
	env.check(child.Id, true, "")
	env.check(parent.Id, false, "timeout")
	env.check(other.Id, false, "timeout")
	env.check(child.Id, false, "timeout")
	env.takeNamed()
	assert.Equal(t, "Router, Switch", env.record(child.Id).GetString("suppressedBy"))

	// Renaming a down parent renames it in its children.
	env.update(parent.Id, map[string]any{"name": "Gateway"})
	assert.Equal(t, "Gateway, Switch", env.record(child.Id).GetString("suppressedBy"))

	// Dropping one parent keeps the other suppressing.
	env.update(child.Id, map[string]any{"dependsOn": []string{other.Id}})
	assert.Equal(t, "Switch", env.record(child.Id).GetString("suppressedBy"))
	assert.Empty(t, env.takeNamed())

	// Removing the last down parent notifies the suppressed down once.
	require.NoError(t, env.app.Delete(env.record(other.Id)))
	env.engine.Remove(other.Id)
	assert.Equal(t, []string{"Web:down"}, env.takeNamed())
	assert.Empty(t, env.record(child.Id).GetString("suppressedBy"))
	assert.False(t, env.engine.Suppressed(child.Id))

	// A client overwriting suppressedBy is restored.
	env.update(parent.Id, map[string]any{"name": "Gateway"})
	env.update(child.Id, map[string]any{"dependsOn": []string{parent.Id}, "suppressedBy": "stale"})
	assert.Equal(t, "Gateway", env.record(child.Id).GetString("suppressedBy"))
	assert.Empty(t, env.takeNamed(), "the child's down was already notified")

	// Disabled children are not shown as suppressed.
	env.update(child.Id, map[string]any{"enabled": false})
	assert.Empty(t, env.record(child.Id).GetString("suppressedBy"))
}

func TestDependencyRestart(t *testing.T) {
	env := newTestEnv(t)
	parent := env.createMonitor(map[string]any{"name": "Router"})
	child := env.createMonitor(map[string]any{"name": "Web", "dependsOn": []string{parent.Id}})
	env.check(parent.Id, true, "")
	env.check(child.Id, true, "")
	env.check(parent.Id, false, "timeout")
	env.check(child.Id, false, "timeout")
	env.takeNamed()

	env.engine = env.newEngine()
	require.NoError(t, env.engine.Load())
	assert.True(t, env.engine.Suppressed(child.Id))
	env.check(parent.Id, true, "")
	assert.Equal(t, []string{"Router:up", "Web:down"}, env.takeNamed())
}

func TestDependencyListener(t *testing.T) {
	env := newTestEnv(t)
	var mu sync.Mutex
	var changed []string
	env.engine = New(env.app, WithNow(env.clock), WithDependencyListener(func(ids []string) {
		mu.Lock()
		changed = append(changed, ids...)
		mu.Unlock()
	}))
	parent := env.createMonitor(map[string]any{"name": "Router"})
	env.check(parent.Id, true, "")
	env.check(parent.Id, false, "timeout")
	env.check(parent.Id, false, "timeout")
	env.check(parent.Id, true, "")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{parent.Id, parent.Id}, changed)
}
