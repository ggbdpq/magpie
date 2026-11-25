package agent

// Qwen Code (`qwen`, QwenLM/qwen-code, a Gemini CLI fork) keeps its
// settings in ~/.qwen/settings.json ($QWEN_HOME when set). 0.x reads the
// models of the user's own from the modelProviders.<protocol> arrays,
// each {baseUrl, envKey, id, name}: id is the model asked for, name what
// its /model picker shows, and envKey names a variable in the same
// file's env object (or ~/.qwen/.env) with the key. The model sessions
// start on is settings.model {name, baseUrl}, baseUrl telling two entries
// of one id apart, and security.auth.selectedType "openai" skips the
// /auth prompt on start.
//
// 1.x (v1.0.0-preview.1 on) reads them from a top-level providers object
// instead, each member {baseUrl, apiKey, model, models}: apiKey is the
// key itself (a ${VAR} reference the CLI resolves from the environment
// is also taken — ~/.qwen/.env among it — but 1.0.0-preview.2 is only
// seen to consume the key written straight, and then the settings file
// stays the only thing a wired session needs), models[] is the catalog
// whose entries' .model is the id its /model picker shows beside the
// provider's, and model is the provider's default model — the pick keyed
// magpie/<wire id>, as model.name is (a bare wire id reads as provider
// <its first part>, which has no member: preview.2, xuanyi883-sudo on
// #6867), else the catalog's first keyed the same (a preview.2 that
// reads no models[] still finds the picked model there). A model name is
// keyed <provider-id>/<model-id> — model.name among it, which 1.x looks
// up in the providers' catalog as that key (0.x matched a bare entry id,
// so a bare wire id there found nothing, and /model showed none of
// magpie's, the report). security.auth.selectedType "openai" is still
// its provider-backed auth type, endpoints and keys from
// settings.providers.
//
// Which form to write is the CLI's version: a 1.x one
// (qwenOneX) gets providers.magpie, a 0.x one modelProviders.openai as
// before. A version magpie cannot learn is taken for 1.x when the file
// has a providers object (0.x's schema has none, and providers.magpie is
// magpie's own wiring, which keeps a wired 1.x on 1.x when its CLI has
// left the PATH), else for 0.x, as fx takes an unknown version for an
// older fx.
//
// magpie appends one entry per catalog model — modelProviders.openai in
// 0.x, providers.magpie's models in 1.x — the wire id the gateway takes
// ("<provider>/<model>") and the name "magpie/<provider>/<model>", every
// one on the same env key of its own, which is how an entry of magpie's
// is told from the user's, and points settings.model and
// security.auth.selectedType at a pick. The key itself lives in the
// settings' env object (0.x); 1.x's member carries it straight. A
// shell-exported or ~/.qwen/.env variable of the same name would win
// over the env object's, which the qwen-specific name makes unlikely.
// The user's entries stay as they were and in front, so their own ids
// don't move, and the default, auth type and any key of theirs they had
// are stashed and put back when magpie steps out. A wiring of magpie's
// older self — 0.x's entries, key and model.baseUrl in a file a 1.x qwen
// now reads — goes whenever magpie next wires the file, either way.

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
)

const (
	qwenModels  = "modelProviders.openai"
	qwenModel   = "model.name"
	qwenModelAt = "model.baseUrl"
	qwenAuth    = "security.auth.selectedType"
	qwenEnv     = "env." + qwenEnvKey
	// qwenProviders is the top-level object Qwen Code 1.x reads its
	// custom providers from, where 0.x read modelProviders; magpie's own
	// is the member named magpieID.
	qwenProviders = "providers"
	// qwenEnvKey names the variable magpie's provider entries take their
	// key from: qwen-specific so a shell or ~/.qwen/.env value of the
	// name is unlikely to shadow the settings' (their priority over it is
	// why "MAGPIE_API_KEY" is not used), and set in the settings' env
	// object while magpie is wired in, which names one of magpie's
	// entries wherever it appears.
	qwenEnvKey = "MAGPIE_QWEN_API_KEY"
)

// qwenCLIVersion is what the qwen CLI on PATH says its version is, ""
// unknown. A var so tests can set it.
var qwenCLIVersion = func() string { return installedVersion("qwen") }

// qwenOneX says whether the qwen whose settings are at path reads 1.x's
// settings (top-level providers, a model keyed provider/model): the CLI's
// version says so. A version magpie cannot learn is taken for 1.x when
// the file has a providers object, else for an older one; a distro's
// version is what its probe found, none asked yet for qwen (so ""), and
// its file speaks there.
func qwenOneX(at place, path string) bool {
	v := at.version
	if at.spell == nil {
		v = qwenCLIVersion()
	}
	if v != "" {
		// its first number says the line: 1.x's schema is the
		// prerelease's too (v1.0.0-preview), which Newer would take for
		// older than the release
		maj, _, _ := strings.Cut(v, ".")
		return maj == "1" || Newer(v, "1.0.0")
	}
	if p, ok := edit.GetJSON(path, qwenProviders); ok {
		return strings.HasPrefix(strings.TrimSpace(p), "{")
	}
	return false
}

// qwenEntry is one modelProviders.openai member magpie writes, fields
// ordered as Qwen Code orders its own (alphabetically).
type qwenEntry struct {
	BaseURL    string   `json:"baseUrl"`
	EnvKey     string   `json:"envKey"`
	Generation *qwenGen `json:"generationConfig,omitempty"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
}

// qwenGen is the generationConfig a model's entry may carry.
type qwenGen struct {
	ContextWindowSize int `json:"contextWindowSize,omitempty"`
}

// qwenEntriesAt are magpie's entries for a qwen reaching the gateway at
// v1.
func qwenEntriesAt(v1 string) []qwenEntry {
	var out []qwenEntry
	for _, m := range sharedModels("qwen") {
		// name is what /model shows, the id beside it when they differ:
		// the model's name as every agent's list calls it, by the Provider
		// in model names setting (magpie/<id> before, which none of the
		// setting's ways changed)
		e := qwenEntry{ID: m.ID, Name: m.Name, BaseURL: v1, EnvKey: qwenEnvKey}
		if m.Context > 0 {
			e.Generation = &qwenGen{ContextWindowSize: m.Context}
		}
		out = append(out, e)
	}
	return out
}

// qwen1xModel is one providers.magpie.models member magpie writes, fields
// as Qwen Code 1.x orders its own.
type qwen1xModel struct {
	Model         string `json:"model"`
	DisplayName   string `json:"displayName,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

// qwen1xProvider is the providers.magpie member magpie writes: baseUrl
// the gateway, apiKey the gateway's key itself (1.x also resolves a
// ${VAR} reference from the environment, but the key written straight is
// what 1.0.0-preview.2 is seen to consume, and keeps the settings file
// the only thing a wired session needs), model the pick keyed
// magpie/<wire id> (the provider's default model, which 1.x reads beside
// the models[] catalog), and models the catalog.
type qwen1xProvider struct {
	BaseURL string        `json:"baseUrl"`
	APIKey  string        `json:"apiKey"`
	Model   string        `json:"model,omitempty"`
	Models  []qwen1xModel `json:"models"`
}

// qwen1xEntriesAt is magpie's providers.magpie for a qwen reaching the
// gateway at v1 with the key at key, 1.x's shape of qwenEntriesAt; pick
// is the wired model's wire id. The member's model is keyed
// magpie/<wire id>, as model.name is: 1.x reads a model name by its
// provider prefix, and preview.2 reports "provider <the id's first
// part> is not available" for a bare wire id (xuanyi883-sudo on #6867,
// whose manual fix was both keyed). "" for none: the catalog's first.
func qwen1xEntriesAt(v1, key, pick string) qwen1xProvider {
	var ms []qwen1xModel
	for _, m := range sharedModels("qwen") {
		// displayName is what /model shows, as 0.x's name was
		e := qwen1xModel{Model: m.ID, DisplayName: m.Name}
		if m.Context > 0 {
			e.ContextWindow = m.Context
		}
		ms = append(ms, e)
	}
	model := pick
	if model == "" && len(ms) > 0 {
		model = ms[0].Model
	}
	if model != "" {
		model = magpieID + "/" + model
	}
	return qwen1xProvider{BaseURL: v1, APIKey: key, Model: model, Models: ms}
}

// qwenCustom is one modelProviders.openai member as it is in the file:
// its raw JSON, and what of it the picker needs.
type qwenCustom struct {
	raw          json.RawMessage
	id, name     string
	base, envKey string
	ours         bool
}

// qwenCustoms reads modelProviders.openai of a settings.json.
func qwenCustoms(path string) []qwenCustom {
	raw, err := edit.Read(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var out []qwenCustom
	all := gjson.GetBytes(jsonc.ToJSONInPlace(raw), qwenModels)
	if !all.IsArray() {
		// no modelProviders (or none of openai): none of magpie's and
		// none of the user's, rather than an error ForEach gives for null
		return nil
	}
	all.ForEach(func(_, v gjson.Result) bool {
		c := qwenCustom{
			raw:    json.RawMessage(v.Raw),
			id:     v.Get("id").String(),
			name:   v.Get("name").String(),
			base:   v.Get("baseUrl").String(),
			envKey: v.Get("envKey").String(),
		}
		c.ours = c.envKey == qwenEnvKey
		out = append(out, c)
		return true
	})
	return out
}

// qwen1xCustom is one top-level providers member as it is in the file:
// its raw JSON, and what of it the picker needs.
type qwen1xCustom struct {
	raw      json.RawMessage
	id, base string
	apiKey   string
	ours     bool
}

// qwen1xCustoms reads providers of a settings.json; key is the gateway's
// key, which magpie's own member carries.
func qwen1xCustoms(path, key string) []qwen1xCustom {
	raw, err := edit.Read(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var out []qwen1xCustom
	all := gjson.GetBytes(jsonc.ToJSONInPlace(raw), qwenProviders)
	if !all.IsObject() {
		// no providers (or an array): none of magpie's and none of the
		// user's
		return nil
	}
	all.ForEach(func(id, v gjson.Result) bool {
		c := qwen1xCustom{
			raw:    json.RawMessage(v.Raw),
			id:     id.String(),
			base:   v.Get("baseUrl").String(),
			apiKey: v.Get("apiKey").String(),
		}
		// ours: the member named magpie carrying the gateway's key, which
		// the user's own would not
		c.ours = c.id == magpieID && c.apiKey == key
		out = append(out, c)
		return true
	})
	return out
}

// qwen1xOurs is magpie's providers.magpie when the file has it, nil when
// it hasn't or it isn't magpie's.
func qwen1xOurs(path, key string) *qwen1xCustom {
	cs := qwen1xCustoms(path, key)
	for i := range cs {
		if cs[i].ours {
			return &cs[i]
		}
	}
	return nil
}

// qwenModelOf reads settings.model: the model's id, and the baseUrl
// telling one of its two entries apart ("" when unset).
func qwenModelOf(path string) (name, base string) {
	name, _ = edit.GetJSON(path, qwenModel)
	base, _ = edit.GetJSON(path, qwenModelAt)
	return name, base
}

// qwenPrune takes a parent key out once the last thing in it is gone: the
// "env": {} or "modelProviders": {} that emptying magpie's writes would
// leave was never the user's.
func qwenPrune(path, parent string) error {
	if v, ok := edit.GetJSON(path, parent); ok && strings.TrimSpace(v) == "{}" {
		return edit.DelJSON(path, parent)
	}
	return nil
}

// qwenFind finds the entry settings.model points at: by id and baseUrl,
// or the first of the id when no baseUrl is set.
func qwenFind(cs []qwenCustom, name, base string) *qwenCustom {
	for i := range cs {
		if cs[i].id == name && (base == "" || cs[i].base == base) {
			return &cs[i]
		}
	}
	return nil
}

func qwen(home string) *Agent { return qwenIn(here(home)) }

// qwenIn is Qwen Code at a place: this machine's home, or a WSL distro's
// (see wsl.go), whose entries name the gateway as the distro reaches it,
// with the key it takes from there.
func qwenIn(at place) *Agent {
	dir := at.join(".qwen")
	if d := at.getenv("QWEN_HOME"); d != "" {
		dir = d
	}
	path := filepath.Join(dir, "settings.json")
	entries := func() []qwenEntry { return qwenEntriesAt(at.v1()) }
	entries1x := func(pick string) qwen1xProvider { return qwen1xEntriesAt(at.v1(), at.gwKey(), pick) }
	keyName := "qwen:" + path + ":" + qwenModel
	keyBase := "qwen:" + path + ":" + qwenModelAt
	keyAuth := "qwen:" + path + ":" + qwenAuth
	// keyAuthSeen marks the user's own auth type already stashed (or
	// known absent), so a later wire doesn't take magpie's own "openai"
	// for it
	keyAuthSeen := keyAuth + ".seen"
	keyEnv := "qwen:" + path + ":" + qwenEnv

	// get spells an entry of magpie's as the catalog does
	get := func() string {
		name, base := qwenModelOf(path)
		if name == "" {
			return ""
		}
		if c := qwenFind(qwenCustoms(path), name, base); c != nil && c.ours {
			return magpieID + "/" + c.id
		}
		return name
	}

	// setEnvKey writes env.MAGPIE_API_KEY as the gateway's key; with=false
	// puts back what was stashed, or takes the key out where it still is
	// magpie's. A value of the user's own under that name is stashed before
	// it is replaced and never removed: magpie only removes what it wrote.
	setEnvKey := func(with bool) error {
		if with {
			if v, ok := edit.GetJSON(path, qwenEnv); ok && v != at.gwKey() {
				stash(map[string]string{keyEnv: v})
			}
			return edit.SetJSON(path, edit.KV{Path: qwenEnv, Value: at.gwKey()})
		}
		if was := unstash(keyEnv); was != "" {
			return edit.SetJSON(path, edit.KV{Path: qwenEnv, Value: was})
		}
		if v, ok := edit.GetJSON(path, qwenEnv); ok && v == at.gwKey() {
			if err := edit.DelJSON(path, qwenEnv); err != nil {
				return err
			}
			return qwenPrune(path, "env")
		}
		return nil
	}

	// setAuth sets security.auth.selectedType to openai while magpie's
	// models are the pick (skipping the /auth prompt), stashing the user's
	// own; with=false puts it back, or takes the key out where it never
	// was, pruning the empty security.auth and security it would leave.
	setAuth := func(with bool) error {
		if with {
			// the user's own value is stashed once, whatever it is —
			// "openai" of their own too, or it can't be told from
			// magpie's on a later wire or Sync and comes off with it
			if _, seen := stashShared()[keyAuthSeen]; !seen {
				kv := map[string]string{keyAuthSeen: "1"}
				if v, ok := edit.GetJSON(path, qwenAuth); ok {
					kv[keyAuth] = v
				}
				stash(kv)
			}
			return edit.SetJSON(path, edit.KV{Path: qwenAuth, Value: "openai"})
		}
		forget(keyAuthSeen)
		if was := unstash(keyAuth); was != "" {
			return edit.SetJSON(path, edit.KV{Path: qwenAuth, Value: was})
		}
		if err := edit.DelJSON(path, qwenAuth); err != nil {
			return err
		}
		if err := qwenPrune(path, "security.auth"); err != nil {
			return err
		}
		return qwenPrune(path, "security")
	}

	// setProviders writes modelProviders.openai with the user's own and,
	// when with, magpie's after them; none left takes the array out. A
	// with=false when none were magpie's changes nothing.
	setProviders := func(with bool) error {
		var ms []any
		var had bool
		for _, c := range qwenCustoms(path) {
			if c.ours {
				had = true
				continue
			}
			ms = append(ms, c.raw)
		}
		if with {
			if err := setEnvKey(true); err != nil {
				return err
			}
			if err := setAuth(true); err != nil {
				return err
			}
			for _, e := range entries() {
				ms = append(ms, e)
			}
		} else if !had {
			// nothing of magpie's left: a key it left behind, and the
			// auth type it set, still go
			if err := setEnvKey(false); err != nil {
				return err
			}
			return setAuth(false)
		}
		if len(ms) > 0 {
			if err := edit.SetJSON(path, edit.KV{Path: qwenModels, Value: ms}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, qwenModels); err != nil {
			return err
		}
		if !with {
			if err := setEnvKey(false); err != nil {
				return err
			}
			if err := setAuth(false); err != nil {
				return err
			}
			return qwenPrune(path, "modelProviders")
		}
		return nil
	}

	// setProviders1x writes providers with the user's own and, when with,
	// magpie's member among them — the member carrying the gateway's key
	// itself and the pick as its model; none left takes the object out. A
	// with=false when none were magpie's changes nothing.
	setProviders1x := func(with bool, pick string) error {
		keep := map[string]json.RawMessage{}
		var had bool
		for _, c := range qwen1xCustoms(path, at.gwKey()) {
			if c.ours {
				had = true
				continue
			}
			keep[c.id] = c.raw
		}
		if with {
			if err := setAuth(true); err != nil {
				return err
			}
			e, err := json.Marshal(entries1x(pick))
			if err != nil {
				return err
			}
			keep[magpieID] = e
		} else if !had {
			// nothing of magpie's left: the auth type it set still goes
			return setAuth(false)
		}
		if len(keep) > 0 {
			if err := edit.SetJSON(path, edit.KV{Path: qwenProviders, Value: keep}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, qwenProviders); err != nil {
			return err
		}
		if !with {
			if err := setAuth(false); err != nil {
				return err
			}
			return qwenPrune(path, qwenProviders)
		}
		return nil
	}

	// dropLegacy takes magpie's 0.x wiring out of a file a 1.x qwen now
	// reads: the entries it wrote in modelProviders.openai (the user's
	// own stay), the key it left in the env object, and the baseUrl
	// beside model.name — 1.x's schema refuses all three, and the key's
	// value of the user's own is put back from where an older magpie
	// stashed it. It runs on both ways, in and out, so such a wiring goes
	// whenever magpie next touches the file.
	dropLegacy := func() error {
		var ms []any
		var had bool
		for _, c := range qwenCustoms(path) {
			if c.ours {
				had = true
				continue
			}
			ms = append(ms, c.raw)
		}
		if had {
			if len(ms) > 0 {
				if err := edit.SetJSON(path, edit.KV{Path: qwenModels, Value: ms}); err != nil {
					return err
				}
			} else if err := edit.DelJSON(path, qwenModels); err != nil {
				return err
			}
		}
		if v, ok := edit.GetJSON(path, qwenEnv); ok && v == at.gwKey() {
			if was := unstash(keyEnv); was != "" {
				if err := edit.SetJSON(path, edit.KV{Path: qwenEnv, Value: was}); err != nil {
					return err
				}
			} else {
				if err := edit.DelJSON(path, qwenEnv); err != nil {
					return err
				}
				if err := qwenPrune(path, "env"); err != nil {
					return err
				}
			}
		}
		if _, ok := edit.GetJSON(path, qwenModelAt); ok {
			if err := edit.DelJSON(path, qwenModelAt); err != nil {
				return err
			}
			if v, ok := edit.GetJSON(path, "model"); ok && strings.TrimSpace(v) == "{}" {
				if err := edit.DelJSON(path, "model"); err != nil {
					return err
				}
			}
		}
		return nil
	}

	// put sets settings.model to (name, base), or takes it out for "":
	// the "model": {} that Qwen Code never wrote is removed with its last
	// member.
	put := func(name, base string) error {
		if name != "" {
			if base != "" {
				if err := edit.SetJSON(path, edit.KV{Path: qwenModelAt, Value: base}); err != nil {
					return err
				}
			} else if err := edit.DelJSON(path, qwenModelAt); err != nil {
				// baseUrl unset stays unset: qwen treats "" as a real
				// endpoint of its own in the composite match
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: qwenModel, Value: name})
		}
		if err := edit.DelJSON(path, qwenModelAt); err != nil {
			return err
		}
		if err := edit.DelJSON(path, qwenModel); err != nil {
			return err
		}
		if v, ok := edit.GetJSON(path, "model"); ok && strings.TrimSpace(v) == "{}" {
			return edit.DelJSON(path, "model")
		}
		return nil
	}

	// put1x sets model.name — a 1.x qwen keys a model provider/model, and
	// its schema takes nothing beside it in model — or takes it out for
	// "", the "model": {} it would leave going with its last member.
	put1x := func(name string) error {
		if name != "" {
			return edit.SetJSON(path, edit.KV{Path: qwenModel, Value: name})
		}
		if err := edit.DelJSON(path, qwenModel); err != nil {
			return err
		}
		if v, ok := edit.GetJSON(path, "model"); ok && strings.TrimSpace(v) == "{}" {
			return edit.DelJSON(path, "model")
		}
		return nil
	}

	// set1x is a 1.x qwen's wiring: magpie's member among the top-level
	// providers, carrying the gateway's key itself and the pick keyed
	// magpie/<wire id> as its model, the auth type openai, and
	// model.name "magpie/<wire id>"; anything else of magpie's — the 0.x
	// wiring its older self left — goes either way. A pick of the user's
	// own, or a reset, takes magpie's wiring out and puts what they had
	// back.
	set1x := func(v string) error {
		if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
			// what the user had goes to the stash unless it was magpie's
			// already
			name, _ := qwenModelOf(path)
			if _, onMagpie := strings.CutPrefix(name, magpieID+"/"); !onMagpie {
				kv := map[string]string{}
				if name != "" {
					kv[keyName] = name
				} else {
					forget(keyName)
				}
				stash(kv)
			}
			if err := setProviders1x(true, ref); err != nil {
				return err
			}
			if err := dropLegacy(); err != nil {
				return err
			}
			return put1x(magpieID + "/" + ref)
		}
		name, _ := qwenModelOf(path)
		_, onMagpie := strings.CutPrefix(name, magpieID+"/")
		if onMagpie && qwen1xOurs(path, at.gwKey()) != nil {
			// back to what the user had, or nothing
			if v == "" {
				v = unstash(keyName)
			} else {
				forget(keyName)
			}
		}
		if err := put1x(v); err != nil {
			return err
		}
		if err := dropLegacy(); err != nil {
			return err
		}
		return setProviders1x(false, "")
	}

	return atomic(&Agent{
		ID: "qwen", Name: "Qwen Code", Icon: "qwen-color", Aliases: []string{"qwen-code", "qwen-cli"}, Spelled: prefixed,
		// qwen's requests carry QwenCode/<version> (packages/core/src/core/
		// openaiContentGenerator/provider/default.ts buildHeaders)
		UA:  []string{"qwencode"},
		Bin: "qwen", Dir: dir, Path: path,
		ListOnly:  true,
		Telemetry: usageStatsTelemetry(at, path, "qwen"),
		Sync: func() error {
			if qwen1xOurs(path, at.gwKey()) != nil {
				// the pick stays the member's model (keyed
				// magpie/<wire id>, as qwen1xEntriesAt writes it)
				// through the refresh; none, the catalog's first
				pick := ""
				if name, _ := qwenModelOf(path); name != "" {
					if ref, ok := strings.CutPrefix(name, magpieID+"/"); ok && isMagpie(ref) {
						pick = ref
					}
				}
				if err := setProviders1x(true, pick); err != nil {
					return err
				}
				return dropLegacy()
			}
			for _, c := range qwenCustoms(path) {
				if c.ours {
					return setProviders(true)
				}
			}
			return nil
		},
		Notice: func() string {
			if Running(`(^|/)qwen( |$)`) {
				return noticeQwen.String()
			}
			return ""
		},
		Check: func() string {
			// settings.model is magpie's by the entry it names, wherever
			// that entry points: another address is drift to report, not
			// a model of the user's
			name, _ := qwenModelOf(path)
			if name == "" {
				return ""
			}
			// a 1.x wiring: model.name keyed provider/model, magpie's
			// member among providers, carrying the gateway's key itself
			if _, onMagpie := strings.CutPrefix(name, magpieID+"/"); onMagpie {
				if mp := qwen1xOurs(path, at.gwKey()); mp != nil {
					if d := wiringOff("Qwen Code", path, func(k string) (string, bool) {
						if k == "apiKey" {
							return mp.apiKey, true
						}
						return mp.base, true
					}, "baseUrl", at.v1(), "apiKey", at.gwKey()); d != "" {
						return d
					}
					if v, ok := edit.GetJSON(path, qwenAuth); !ok || v != "openai" {
						return "Qwen Code's security.auth.selectedType (settings.json) no longer is openai, so it asks /auth instead of using magpie"
					}
					return ""
				}
			}
			var c *qwenCustom
			cs := qwenCustoms(path)
			for i := range cs {
				if cs[i].ours && cs[i].id == name {
					c = &cs[i]
					break
				}
			}
			if c == nil {
				return ""
			}
			if v, _ := edit.GetJSON(path, qwenEnv); !keyHolds(v, at.gwKey()) {
				return "Qwen Code's " + qwenEnv + " (settings.json) no longer is magpie's key, so magpie's models have none"
			}
			if v, ok := edit.GetJSON(path, qwenAuth); !ok || v != "openai" {
				return "Qwen Code's security.auth.selectedType (settings.json) no longer is openai, so it asks /auth instead of using magpie"
			}
			return wiringOff("Qwen Code", path, func(k string) (string, bool) {
				g := gjson.GetBytes(c.raw, k)
				return g.String(), g.Exists()
			}, "baseUrl", at.v1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				if qwenOneX(at, path) {
					return set1x(v)
				}
				curName, curBase := qwenModelOf(path)
				ours := qwenFind(qwenCustoms(path), curName, curBase)
				onOurs := ours != nil && ours.ours
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !onOurs {
						kv := map[string]string{keyBase: curBase}
						if curName != "" {
							kv[keyName] = curName
						} else {
							forget(keyName)
						}
						stash(kv)
					}
					if err := setProviders(true); err != nil {
						return err
					}
					return put(ref, at.v1())
				}
				base := ""
				fromStash := false
				if onOurs {
					// back to what the user had, name and its baseUrl
					if v == "" {
						v, base, fromStash = unstash(keyName), unstash(keyBase), true
					} else {
						forget(keyName, keyBase)
					}
				}
				if v != "" && !fromStash {
					if c := qwenFind(qwenCustoms(path), v, ""); c != nil {
						base = c.base
					}
				}
				if err := put(v, base); err != nil {
					return err
				}
				return setProviders(false)
			},
			Options: func(cur map[string]string) []Option {
				return withVia(qwenOwnOptions(path, at.gwKey(), cur["model"]), "qwen", magpieID+"/")
			},
		}},
	}, path)
}

// qwenOwnOptions are the models of the user's own the settings file has:
// 1.x's providers (each member's models[].model, keyed provider/model as
// 1.x keys a model of its own) and 0.x's modelProviders.openai entries
// that aren't magpie's, and the current pick; key is the gateway's, by
// which magpie's own member is told from theirs.
func qwenOwnOptions(path, key, cur string) []Option {
	seen := map[string]bool{}
	var out []Option
	for _, c := range qwen1xCustoms(path, key) {
		if c.ours {
			continue
		}
		ms := gjson.GetBytes(c.raw, "models")
		if !ms.IsArray() {
			continue
		}
		ms.ForEach(func(_, m gjson.Result) bool {
			id := m.Get("model").String()
			if id == "" || seen[c.id+"/"+id] {
				return true
			}
			seen[c.id+"/"+id] = true
			label := m.Get("displayName").String()
			if label == "" || label == id {
				label = ""
			}
			out = append(out, Option{Value: c.id + "/" + id, Label: label, Icon: modelIcon("", id)})
			return true
		})
	}
	for _, c := range qwenCustoms(path) {
		if c.ours || seen[c.id] {
			continue
		}
		seen[c.id] = true
		label := c.name
		if label == "" || label == c.id {
			label = ""
		}
		out = append(out, Option{Value: c.id, Label: label, Icon: modelIcon("", c.id)})
	}
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur)}}, out...)
	}
	return group("Qwen Code", out)
}

// what qwen says after a change (notice.go)
var (
	noticeQwen = newNotice("Qwen Code reads a session's model at start-up — start a new session, or /model anew, to use this.")
)
