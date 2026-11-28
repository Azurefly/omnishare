package aiquota

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zalando/go-keyring"
	"omnishare/internal/durable"
)

type Account struct {
	ID          string   `json:"id"`
	Provider    string   `json:"provider"`
	Alias       string   `json:"alias"`
	Shared      bool     `json:"shared"`
	SessionOnly bool     `json:"session_only,omitempty"`
	Removing    bool     `json:"removing,omitempty"`
	Reading     *Reading `json:"reading,omitempty"`
	Problem     string   `json:"problem,omitempty"`
}
type savedAccount struct {
	Account
	Site     string `json:"site,omitempty"`
	Resource string `json:"resource,omitempty"`
	Owner    string `json:"owner,omitempty"`
}
type Input struct {
	Provider    string `json:"provider"`
	Alias       string `json:"alias"`
	Site        string `json:"site"`
	Resource    string `json:"resource"`
	Key         string `json:"key"`
	SessionOnly bool   `json:"session_only"`
}
type Manager struct {
	mu       sync.Mutex
	path     string
	service  string
	accounts map[string]savedAccount
	session  map[string]string
	next     map[string]time.Time
	busy     map[string]bool
	loadErr  error
	client   *http.Client
	read     func(context.Context, *http.Client, Provider, string, string) (Reading, error)
}

func Open(dir, node string) *Manager {
	m := &Manager{path: filepath.Join(dir, "ai-quota-accounts.json"), service: "OmniShare.AIQuota." + identity(node + "|" + filepath.Clean(dir))[:24], accounts: map[string]savedAccount{}, session: map[string]string{}, next: map[string]time.Time{}, busy: map[string]bool{}, read: query,
		client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	b, e := os.ReadFile(m.path)
	if os.IsNotExist(e) {
		return m
	}
	var list []savedAccount
	if e != nil || len(b) > 4<<20 || json.Unmarshal(b, &list) != nil || len(list) > 48 {
		m.loadErr = errors.New("本机额度配置无法读取")
		return m
	}
	for _, a := range list {
		if a.ID != "" && !a.SessionOnly {
			m.accounts[a.ID] = a
		}
	}
	return m
}
func (m *Manager) saveLocked() error {
	list := make([]savedAccount, 0, len(m.accounts))
	for _, a := range m.accounts {
		if !a.SessionOnly {
			list = append(list, a)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, e := json.Marshal(list)
	if e != nil {
		return e
	}
	if durable.WriteFile(m.path, data, 0600) != nil {
		return errors.New("无法保存本机额度配置")
	}
	return nil
}
func (m *Manager) List(shared bool) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Account, 0, len(m.accounts))
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	for _, a := range m.accounts {
		if shared && (!a.Shared || a.Reading == nil) {
			continue
		}
		entry := a.Account
		// The remote view only receives the explicitly shared reading and label.
		if shared {
			if entry.Problem != "" {
				entry.Problem = "来源设备上次刷新失败，显示上次成功读数"
			}
			entry.SessionOnly = false
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Alias == result[j].Alias {
			return result[i].ID < result[j].ID
		}
		return result[i].Alias < result[j].Alias
	})
	return result, nil
}
func (a savedAccount) target() string {
	if a.Resource != "" {
		return a.Resource
	}
	return a.Site
}
func secretRef(a savedAccount) string { return identity(a.ID + "|" + a.Provider + "|" + a.target()) }
func (m *Manager) Create(ctx context.Context, in Input) (Account, error) {
	p, ok := findProvider(in.Provider)
	if !ok {
		return Account{}, errors.New("未知的额度来源")
	}
	alias := strings.TrimSpace(in.Alias)
	if alias == "" {
		alias = p.Name
	}
	if !shortText(alias, 60) {
		return Account{}, errors.New("账号别名不能超过 60 字")
	}
	site := ""
	resource := strings.TrimSpace(in.Resource)
	if e := validateResource(p.ID, resource); e != nil {
		return Account{}, e
	}
	if p.ID == "gemini" {
		in.SessionOnly = true
	}
	var e error
	if p.Site {
		site, e = normalizeSite(in.Site)
		if e != nil {
			return Account{}, e
		}
	} else if in.Site != "" {
		return Account{}, errors.New("此来源使用固定的服务地址")
	}
	key := strings.TrimSpace(in.Key)
	if p.Local {
		if key != "" {
			return Account{}, errors.New("本机客户端自行管理登录")
		}
		in.SessionOnly = false
	} else if len(key) == 0 || len(key) > 8192 || strings.IndexFunc(key, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return Account{}, errors.New("请输入所选服务的有效密钥")
	}
	m.mu.Lock()
	initialErr := m.loadErr
	m.mu.Unlock()
	if initialErr != nil {
		return Account{}, initialErr
	}
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	target := site
	if resource != "" {
		target = resource
	}
	reading, e := m.read(ctx, m.client, p, target, key)
	if e != nil {
		return Account{}, e
	}
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		return Account{}, errors.New("无法生成账号标识")
	}
	a := savedAccount{Account: Account{ID: hex.EncodeToString(random[:]), Provider: p.ID, Alias: alias, SessionOnly: in.SessionOnly, Reading: &reading}, Site: site, Resource: resource, Owner: reading.owner}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.accounts) >= 48 {
		return Account{}, errors.New("本机最多连接 48 个额度账号")
	}
	for _, prior := range m.accounts {
		if prior.Provider == p.ID && prior.target() == target && (prior.Alias == alias || p.Local && a.Owner == prior.Owner) {
			return Account{}, errors.New("此来源的账号已连接，请更换别名或刷新已有账号")
		}
	}
	if !p.Local && !in.SessionOnly {
		if keyring.Set(m.service, secretRef(a), key) != nil {
			return Account{}, errors.New("系统凭据库不可用，请解锁后重试，或选择仅本次会话")
		}
	}
	m.accounts[a.ID] = a
	if in.SessionOnly {
		m.session[a.ID] = key
	} else if e = m.saveLocked(); e != nil {
		delete(m.accounts, a.ID)
		if !p.Local {
			_ = keyring.Delete(m.service, secretRef(a))
		}
		return Account{}, e
	}
	return a.Account, nil
}

var ErrMissing = errors.New("额度账号不存在")
var errRemoving = errors.New("移除尚未完成，请再次点击移除")

func (m *Manager) Refresh(ctx context.Context, id string) (Account, error) {
	m.mu.Lock()
	a, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return Account{}, ErrMissing
	}
	if a.Removing {
		m.mu.Unlock()
		return Account{}, errRemoving
	}
	if m.busy[id] || time.Now().Before(m.next[id]) {
		m.mu.Unlock()
		return Account{}, errors.New("额度正在刷新或处于冷却期，请稍后重试")
	}
	m.busy[id] = true
	m.next[id] = time.Now().Add(5 * time.Second)
	key := m.session[id]
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.busy, id); m.mu.Unlock() }()
	p, _ := findProvider(a.Provider)
	var e error
	if !p.Local && !a.SessionOnly {
		key, e = keyring.Get(m.service, secretRef(a))
		if e != nil {
			e = errors.New("无法读取系统凭据，请解锁凭据库或重新连接")
		}
	}
	var r Reading
	if e == nil {
		c, cancel := context.WithTimeout(ctx, 22*time.Second)
		defer cancel()
		r, e = m.read(c, m.client, p, a.target(), key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.accounts[id]
	if !ok {
		return Account{}, ErrMissing
	}
	if current.Removing {
		return Account{}, errRemoving
	}
	old := current
	if e == nil && current.Owner != "" && current.Owner != r.owner {
		e = errChanged
	}
	if e != nil {
		current.Problem = e.Error()
		if errors.Is(e, errChanged) || errors.Is(e, errLogin) {
			current.Shared = false
			current.Reading = nil
		}
		var remote *ProviderError
		if errors.As(e, &remote) && remote.Retry > 0 {
			m.next[id] = time.Now().Add(remote.Retry)
		}
	} else {
		current.Reading = &r
		current.Problem = ""
	}
	m.accounts[id] = current
	if !current.SessionOnly {
		if err := m.saveLocked(); err != nil {
			m.accounts[id] = old
			return Account{}, err
		}
	}
	return current.Account, e
}
func (m *Manager) SetShared(id string, share bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[id]
	if !ok {
		return ErrMissing
	}
	if a.Removing {
		return errRemoving
	}
	p, _ := findProvider(a.Provider)
	if share && (a.Reading == nil || p.Local && a.Owner == "") {
		return errors.New("此账号没有可核实归属的额度记录")
	}
	old := a
	a.Shared = share
	m.accounts[id] = a
	if !a.SessionOnly {
		if e := m.saveLocked(); e != nil {
			m.accounts[id] = old
			return e
		}
	}
	return nil
}
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[id]
	if !ok {
		return nil
	}
	p, _ := findProvider(a.Provider)
	if !a.SessionOnly && !p.Local {
		// A retry after a locked vault, disk error, or restart can finish
		// deletion using this record even if the credential is already gone.
		if !a.Removing {
			previous := a
			a.Removing, a.Shared, a.Reading, a.Problem = true, false, nil, errRemoving.Error()
			m.accounts[id] = a
			if e := m.saveLocked(); e != nil {
				m.accounts[id] = previous
				return e
			}
		}
		if e := keyring.Delete(m.service, secretRef(a)); e != nil && !errors.Is(e, keyring.ErrNotFound) {
			return errors.New("系统凭据尚未移除，请解锁凭据库后重试")
		}
	}
	delete(m.accounts, id)
	if !a.SessionOnly {
		if e := m.saveLocked(); e != nil {
			m.accounts[id] = a
			return e
		}
	}
	delete(m.session, id)
	delete(m.next, id)
	return nil
}
