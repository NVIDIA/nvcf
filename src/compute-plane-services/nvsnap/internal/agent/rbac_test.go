package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// agentPackages are the packages of the agent binary that call the
// Kubernetes API with the agent's service account. Their typed client calls
// must all be allowed by the chart's agent ClusterRole: a missing verb only
// shows up on a cluster, as a "forbidden" error, because the unit tests use a
// fake client that allows everything.
var agentPackages = []string{
	".",
	"../checkpointstore",
	"../election",
	"../modelvolume",
	"../rootfsonly",
	"../tenant",
	"../webhook",
}

const agentRBACTemplate = "../../deploy/helm/nvsnap/templates/agent-rbac.yaml"

// clientGroups maps a typed clientset's group accessor to its API group.
var clientGroups = map[string]string{
	"CoreV1":         "",
	"AppsV1":         "apps",
	"BatchV1":        "batch",
	"StorageV1":      "storage.k8s.io",
	"CoordinationV1": "coordination.k8s.io",
	"DiscoveryV1":    "discovery.k8s.io",
	"SnapshotV1":     "snapshot.storage.k8s.io",
}

// clientVerbs maps a typed client method to the RBAC verb it needs.
var clientVerbs = map[string]string{
	"Get":              "get",
	"List":             "list",
	"Watch":            "watch",
	"Create":           "create",
	"Update":           "update",
	"UpdateStatus":     "update",
	"Patch":            "patch",
	"Apply":            "patch",
	"Delete":           "delete",
	"DeleteCollection": "deletecollection",
}

type rbacNeed struct{ group, resource, verb, where string }

// typedClientNeeds finds every typed client call of the form
// <client>.<Group>().<Resource>(ns).<Verb>(...), directly or through a
// variable assigned <client>.<Group>().<Resource>(ns) in the same function.
func typedClientNeeds(t *testing.T, dir string) []rbacNeed {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("%s: no Go sources (missing from the test's data?)", dir)
	}
	var needs []rbacNeed
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Variables holding a resource client, by name.
			vars := map[string][2]string{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
					if id, ok := as.Lhs[0].(*ast.Ident); ok {
						if g, r, ok := resourceClient(as.Rhs[0]); ok {
							vars[id.Name] = [2]string{g, r}
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				verb, ok := clientVerbs[sel.Sel.Name]
				if !ok {
					return true
				}
				group, resource, ok := resourceClient(sel.X)
				if !ok {
					id, isIdent := sel.X.(*ast.Ident)
					if !isIdent {
						return true
					}
					gr, known := vars[id.Name]
					if !known {
						return true
					}
					group, resource = gr[0], gr[1]
				}
				if sel.Sel.Name == "UpdateStatus" {
					resource += "/status"
				}
				// Patch(ctx, name, type, data, opts, subresources...).
				if sel.Sel.Name == "Patch" && len(call.Args) > 5 {
					if lit, ok := call.Args[5].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if sub, err := strconv.Unquote(lit.Value); err == nil {
							resource += "/" + sub
						}
					}
				}
				needs = append(needs, rbacNeed{group, resource, verb, fset.Position(call.Pos()).String()})
				return true
			})
		}
	}
	return needs
}

// resourceClient recognises <x>.<Group>().<Resource>(...) and returns the
// API group and the lower-case resource name.
func resourceClient(e ast.Expr) (group, resource string, ok bool) {
	call, isCall := e.(*ast.CallExpr)
	if !isCall {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	inner, isCall := sel.X.(*ast.CallExpr)
	if !isCall {
		return "", "", false
	}
	gsel, isSel := inner.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	g, known := clientGroups[gsel.Sel.Name]
	if !known {
		return "", "", false
	}
	return g, strings.ToLower(sel.Sel.Name), true
}

var (
	templateLine = regexp.MustCompile(`^\s*\{\{.*\}\}\s*$`)
	templateExpr = regexp.MustCompile(`\{\{[^}]*\}\}`)
)

// agentRules reads the rules of the chart's nvsnap-agent ClusterRole.
func agentRules(t *testing.T) map[[2]string]map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(agentRBACTemplate)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if templateLine.MatchString(l) {
			continue
		}
		lines = append(lines, templateExpr.ReplaceAllString(l, "templated"))
	}
	allowed := map[[2]string]map[string]bool{}
	found := false
	for _, doc := range strings.Split(strings.Join(lines, "\n"), "\n---") {
		var role struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Rules []struct {
				APIGroups []string `json:"apiGroups"`
				Resources []string `json:"resources"`
				Verbs     []string `json:"verbs"`
			} `json:"rules"`
		}
		if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
			t.Fatalf("parse %s: %v", agentRBACTemplate, err)
		}
		if role.Kind != "ClusterRole" || role.Metadata.Name != "nvsnap-agent" {
			continue
		}
		found = true
		for _, r := range role.Rules {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					k := [2]string{g, res}
					if allowed[k] == nil {
						allowed[k] = map[string]bool{}
					}
					for _, v := range r.Verbs {
						allowed[k][v] = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("%s: no ClusterRole nvsnap-agent", agentRBACTemplate)
	}
	return allowed
}

// TestAgentRBACCoversItsClientCalls: every typed Kubernetes call the agent
// makes is allowed by its ClusterRole in the chart. The function-status
// labels were written with a configmap patch the role did not allow, which
// only a cluster showed.
func TestAgentRBACCoversItsClientCalls(t *testing.T) {
	allowed := agentRules(t)
	var missing []string
	total := 0
	for _, dir := range agentPackages {
		for _, n := range typedClientNeeds(t, dir) {
			total++
			if allowed[[2]string{n.group, n.resource}][n.verb] || allowed[[2]string{n.group, n.resource}]["*"] {
				continue
			}
			missing = append(missing, n.verb+" "+n.group+"/"+n.resource+" ("+n.where+")")
		}
	}
	if total == 0 {
		t.Fatal("found no typed client calls; the scan is broken")
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("the agent ClusterRole does not allow %s", m)
	}
}
