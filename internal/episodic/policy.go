package episodic

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/open-policy-agent/opa/v1/rego"
)

// KindIntersection is the typed result of IntersectKindSelectors.
// Empty=true means the caller and policy selectors are incompatible and no
// memories can possibly match. Callers must check Empty before querying the
// store and return an empty result set immediately without any store query.
// When Empty=false, Selector is the effective kind selector to pass to the store
// (empty string means "all kinds").
type KindIntersection struct {
	Selector string
	Empty    bool
}

// ValidateKindSelector validates a user-supplied kind selector string.
// Valid values are: "" (empty = all kinds), a family name (e.g. "default"),
// or a canonical exact name (e.g. "default/v1").
// Returns an error if the selector is non-empty but malformed.
func ValidateKindSelector(sel string) error {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return nil // empty = all kinds
	}
	if strings.Contains(sel, "/") {
		_, _, err := ParseCanonicalKindName(sel)
		if err != nil {
			return fmt.Errorf("invalid kind selector %q: %w", sel, err)
		}
		return nil
	}
	// Family selector.
	if !isValidSchemaComponent(sel) {
		return fmt.Errorf("invalid kind selector %q: must be 1–63 lowercase letters/digits/hyphens starting with a letter", sel)
	}
	return nil
}

// IntersectKindSelectors computes the narrowing intersection of a caller kind
// selector and a policy kind selector.
//
// Both inputs must already be trimmed and valid. Callers should validate the
// caller selector with ValidateKindSelector before calling this function.
//
// Rules (policy never broadens):
//   - callerSel == "" && policySel == ""  → {Selector: "", Empty: false} (no restriction)
//   - callerSel == "" && policySel != ""  → {policySel, false} (policy restricts caller)
//   - callerSel != "" && policySel == ""  → {callerSel, false} (no policy restriction)
//   - equal strings                        → {callerSel, false}
//   - both exact, same string             → {callerSel, false}
//   - both exact, different               → {Empty: true}
//   - callerSel exact, policySel family:
//     caller family == policy              → {callerSel, false}
//     different                            → {Empty: true}
//   - callerSel family, policySel exact:
//     policy family == caller              → {policySel, false}
//     different                            → {Empty: true}
//   - both family, equal                  → {callerSel, false}
//   - both family, different              → {Empty: true}
func IntersectKindSelectors(callerSel, policySel string) KindIntersection {
	callerSel = strings.TrimSpace(callerSel)
	policySel = strings.TrimSpace(policySel)

	if policySel == "" {
		return KindIntersection{Selector: callerSel} // no policy restriction → keep caller
	}
	if callerSel == "" {
		return KindIntersection{Selector: policySel} // no caller restriction → apply policy
	}
	if callerSel == policySel {
		return KindIntersection{Selector: callerSel} // identical → no change
	}

	callerIsExact := strings.Contains(callerSel, "/")
	policyIsExact := strings.Contains(policySel, "/")

	if callerIsExact && policyIsExact {
		// Both exact — must be identical (already checked above).
		return KindIntersection{Empty: true}
	}
	if callerIsExact && !policyIsExact {
		// Caller is exact "family/version", policy is family.
		callerFamily := callerSel[:strings.Index(callerSel, "/")]
		if callerFamily == policySel {
			return KindIntersection{Selector: callerSel} // policy allows whole family; exact is within it
		}
		return KindIntersection{Empty: true}
	}
	if !callerIsExact && policyIsExact {
		// Caller is family, policy is exact "family/version".
		policyFamily := policySel[:strings.Index(policySel, "/")]
		if policyFamily == callerSel {
			return KindIntersection{Selector: policySel} // narrow to exact within caller's family
		}
		return KindIntersection{Empty: true}
	}
	// Both are family selectors — already checked equality above.
	return KindIntersection{Empty: true}
}

// PolicyContext contains the caller's identity for OPA policy evaluation.
type PolicyContext struct {
	UserID    string                 `json:"user_id"`
	ClientID  string                 `json:"client_id"`
	JWTClaims map[string]interface{} `json:"jwt_claims"`
}

// AuthzDecision is the structured authz policy result.
type AuthzDecision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// PolicyEngine evaluates the two immutable OPA policies for episodic memory:
//  1. Authz policy — controls read/write/delete access per (namespace, key).
//  2. Search filter injection policy — narrows namespace_prefix + adds attribute_filter constraints.
//
// Attribute projection is now handled per-memory by the stored MemoryKindVersion
// and is not part of the global PolicyEngine.
type PolicyEngine struct {
	mu           sync.RWMutex
	authz        *rego.PreparedEvalQuery
	filterInject *rego.PreparedEvalQuery
	authzSrc     string
	filterSrc    string
}

// Default built-in Rego policies (used when no policy directory is configured).

//go:embed default-v1/authz.rego
var defaultAuthzRego string

//go:embed default-v1/filter.rego
var defaultFilterInjectRego string

// NewPolicyEngine creates a PolicyEngine. A policy import directory may contain
// both authz.rego and filter.rego at its root to replace the built-in global
// policies. When neither file is present, the built-in policies are used. Other
// Rego files are assets for manifest-based policy types and are not loaded here.
func NewPolicyEngine(ctx context.Context, policyImportDir string) (*PolicyEngine, error) {
	e := &PolicyEngine{}
	if err := e.load(ctx, policyImportDir); err != nil {
		return nil, err
	}
	return e, nil
}

func regoSource(policyImportDir, filename string) (string, error) {
	data, err := os.ReadFile(filepath.Join(policyImportDir, filename))
	if err != nil {
		return "", fmt.Errorf("policy import directory requires %s: %w", filename, err)
	}
	return string(data), nil
}

func (e *PolicyEngine) load(ctx context.Context, policyImportDir string) error {
	policyImportDir = strings.TrimSpace(policyImportDir)
	authzSrc, filterSrc := defaultAuthzRego, defaultFilterInjectRego
	if policyImportDir != "" {
		entries, err := os.ReadDir(policyImportDir)
		if err != nil {
			return fmt.Errorf("read policy import directory: %w", err)
		}
		hasAuthz, hasFilter := false, false
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".rego" {
				continue
			}
			switch entry.Name() {
			case "authz.rego":
				hasAuthz = true
			case "filter.rego":
				hasFilter = true
			}
		}
		if hasAuthz || hasFilter {
			if !hasAuthz {
				return fmt.Errorf("policy import directory requires authz.rego when filter.rego is present")
			}
			if !hasFilter {
				return fmt.Errorf("policy import directory requires filter.rego when authz.rego is present")
			}
			authzSrc, err = regoSource(policyImportDir, "authz.rego")
			if err != nil {
				return err
			}
			filterSrc, err = regoSource(policyImportDir, "filter.rego")
			if err != nil {
				return err
			}
		}
	}

	var err error
	e.authz, err = prepareQuery(ctx, authzSrc, "data.memories.authz.decision")
	if err != nil {
		return fmt.Errorf("episodic: load authz policy: %w", err)
	}
	e.filterInject, err = prepareQuery(ctx, filterSrc, "data.memories.filter")
	if err != nil {
		return fmt.Errorf("episodic: load filter injection policy: %w", err)
	}
	e.authzSrc = authzSrc
	e.filterSrc = filterSrc
	return nil
}

func prepareQuery(ctx context.Context, src, query string) (*rego.PreparedEvalQuery, error) {
	r := rego.New(
		rego.Query(query),
		rego.Module("policy.rego", src),
	)
	pq, err := r.PrepareForEval(ctx)
	if err != nil {
		return nil, err
	}
	return &pq, nil
}

// EvaluateAuthz evaluates the authz policy and returns the decision.
// kind is the resolved exact canonical memory kind (e.g. "default/v1").
// It is passed as input.kind so authz policies can restrict access by kind.
// For read/update it is the kind stored on the row; for write it is the
// resolved write kind (before persistence).
func (e *PolicyEngine) EvaluateAuthz(ctx context.Context, operation string, namespace []string, key string, value map[string]interface{}, index map[string]string, kind string, pc PolicyContext) (AuthzDecision, error) {
	e.mu.RLock()
	q := *e.authz
	e.mu.RUnlock()

	input := map[string]interface{}{
		"operation": operation,
		"namespace": namespace,
		"key":       key,
		"kind":      kind,
		"context":   policyContextToMap(pc),
	}
	if operation == "write" {
		input["value"] = value
		input["index"] = index
	}
	results, err := q.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return AuthzDecision{}, fmt.Errorf("episodic authz eval: %w", err)
	}
	if len(results) == 0 || len(results[0].Expressions) == 0 {
		return AuthzDecision{Allow: false, Reason: "access denied"}, nil
	}

	raw, _ := results[0].Expressions[0].Value.(map[string]interface{})
	if raw == nil {
		return AuthzDecision{Allow: false, Reason: "access denied"}, nil
	}
	decision := AuthzDecision{}
	if allow, ok := raw["allow"].(bool); ok {
		decision.Allow = allow
	}
	if reason, ok := raw["reason"].(string); ok {
		decision.Reason = reason
	}
	if !decision.Allow && strings.TrimSpace(decision.Reason) == "" {
		decision.Reason = "access denied"
	}
	return decision, nil
}

// IsAllowed evaluates the authz policy and returns true if the operation is allowed.
func (e *PolicyEngine) IsAllowed(ctx context.Context, operation string, namespace []string, key string, pc PolicyContext) (bool, error) {
	decision, err := e.EvaluateAuthz(ctx, operation, namespace, key, nil, nil, "", pc)
	if err != nil {
		return false, err
	}
	return decision.Allow, nil
}

// InjectFilter evaluates the search filter injection policy and returns the
// effective namespace_prefix and merged attribute_filter to use for search.
func (e *PolicyEngine) InjectFilter(ctx context.Context, nsPrefix []string, filter map[string]interface{}, pc PolicyContext) ([]string, map[string]interface{}, error) {
	effectivePrefix, policyFilter, _, err := e.InjectFilterPartsWithKind(ctx, nsPrefix, filter, "", pc)
	if err != nil {
		return nsPrefix, filter, err
	}
	merged := make(map[string]interface{})
	for k, v := range filter {
		merged[k] = v
	}
	for k, v := range policyFilter {
		merged[k] = v
	}
	return effectivePrefix, merged, nil
}

// InjectFilterParts evaluates the search filter injection policy and returns
// the effective namespace_prefix plus policy-supplied attribute_filter without
// merging it into the caller filter.  The caller kind selector is not passed
// through this variant; use InjectFilterPartsWithKind when kind restriction is needed.
func (e *PolicyEngine) InjectFilterParts(ctx context.Context, nsPrefix []string, filter map[string]interface{}, pc PolicyContext) ([]string, map[string]interface{}, error) {
	effectivePrefix, policyFilter, _, err := e.InjectFilterPartsWithKind(ctx, nsPrefix, filter, "", pc)
	return effectivePrefix, policyFilter, err
}

// InjectFilterPartsWithKind evaluates the search filter injection policy and returns:
//   - effectivePrefix: narrowed namespace prefix
//   - policyFilter:    policy-injected attribute_filter (not yet merged with caller)
//   - ki:              KindIntersection of callerKind and the optional policy kind output
//
// filter.rego may include a "kind" field in its output document; if present it
// must be a valid kind selector string and is intersected with callerKind using
// IntersectKindSelectors.  A non-string or malformed policy kind output returns an
// internal error.  The built-in default policy does not output "kind", so
// ki.Selector == callerKind and ki.Empty == false for all built-in policy users.
func (e *PolicyEngine) InjectFilterPartsWithKind(ctx context.Context, nsPrefix []string, filter map[string]interface{}, callerKind string, pc PolicyContext) (effectivePrefix []string, policyFilter map[string]interface{}, ki KindIntersection, err error) {
	e.mu.RLock()
	q := *e.filterInject
	e.mu.RUnlock()

	input := map[string]interface{}{
		"namespace_prefix": nsPrefix,
		"filter":           filter,
		"kind":             callerKind,
		"context":          policyContextToMap(pc),
	}
	results, evalErr := q.Eval(ctx, rego.EvalInput(input))
	if evalErr != nil {
		return nsPrefix, nil, KindIntersection{Selector: callerKind}, fmt.Errorf("episodic filter inject eval: %w", evalErr)
	}
	if len(results) == 0 || len(results[0].Expressions) == 0 {
		return nsPrefix, nil, KindIntersection{Selector: callerKind}, nil
	}
	m, ok := results[0].Expressions[0].Value.(map[string]interface{})
	if !ok || m == nil {
		return nsPrefix, nil, KindIntersection{Selector: callerKind},
			fmt.Errorf("episodic policy error: filter.rego result must be an object, got %T", results[0].Expressions[0].Value)
	}

	// Extract effective namespace_prefix.
	effectivePrefix = nsPrefix
	if raw, ok := m["namespace_prefix"]; ok {
		var valid bool
		effectivePrefix, valid = strictStringSlice(raw)
		if !valid {
			return nsPrefix, nil, KindIntersection{Selector: callerKind},
				fmt.Errorf("episodic policy error: filter.rego 'namespace_prefix' output must be an array of strings, got %T", raw)
		}
	}

	outFilter := make(map[string]interface{})
	if rawAF, present := m["attribute_filter"]; present {
		af, valid := rawAF.(map[string]interface{})
		if !valid || af == nil {
			return nsPrefix, nil, KindIntersection{Selector: callerKind},
				fmt.Errorf("episodic policy error: filter.rego 'attribute_filter' output must be an object, got %T", rawAF)
		}
		for k, v := range af {
			outFilter[k] = v
		}
	}

	// Extract optional policy kind narrowing.
	// If "kind" is present in the policy output it must be a non-empty string
	// that passes ValidateKindSelector; anything else is a policy error.
	// A present-but-empty string (after trim) is also an error: omit the key to
	// express "no restriction" rather than returning an empty string.
	policyKind := ""
	if rawPK, hasPK := m["kind"]; hasPK {
		pk, ok := rawPK.(string)
		if !ok {
			return nsPrefix, nil, KindIntersection{Selector: callerKind},
				fmt.Errorf("episodic policy error: filter.rego 'kind' output must be a non-empty string, got %T", rawPK)
		}
		pk = strings.TrimSpace(pk)
		if pk == "" {
			return nsPrefix, nil, KindIntersection{Selector: callerKind},
				fmt.Errorf("episodic policy error: filter.rego 'kind' output must be non-empty when present (omit the key to express no restriction)")
		}
		if err2 := ValidateKindSelector(pk); err2 != nil {
			return nsPrefix, nil, KindIntersection{Selector: callerKind},
				fmt.Errorf("episodic policy error: malformed filter.rego 'kind' output: %w", err2)
		}
		policyKind = pk
	}
	ki = IntersectKindSelectors(callerKind, policyKind)

	return effectivePrefix, outFilter, ki, nil
}

func strictStringSlice(v interface{}) ([]string, bool) {
	switch values := v.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []interface{}:
		out := make([]string, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			out[i] = text
		}
		return out, true
	default:
		return nil, false
	}
}

func policyContextToMap(pc PolicyContext) map[string]interface{} {
	claims := pc.JWTClaims
	if claims == nil {
		claims = map[string]interface{}{}
	}
	return map[string]interface{}{
		"user_id":    pc.UserID,
		"client_id":  pc.ClientID,
		"jwt_claims": claims,
	}
}

func toStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}
