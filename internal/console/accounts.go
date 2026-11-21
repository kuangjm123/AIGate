package console

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/kuangjm123/AIGate/internal/agent"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/respond"
	"github.com/kuangjm123/AIGate/internal/state"
)

func (s *Server) listAccounts(w http.ResponseWriter) int {
	if s.Logins == nil {
		respond.JSON(w, http.StatusOK, map[string]any{"status": "off", "accounts": []any{}})
		return http.StatusOK
	}
	snap := login.Discover(s.Logins())
	rows := make([]map[string]any, 0, len(snap.Accounts))
	for _, account := range snap.Accounts {
		models := []string{}
		if account.ModelsKnown {
			models = make([]string, 0, len(account.Models))
			for _, model := range account.Models {
				models = append(models, login.CatalogID(account.ID, model.ID))
			}
		}
		rows = append(rows, map[string]any{
			"id": account.ID, "name": account.Name, "source": "login",
			"plugin": account.Plugin, "readable": account.Readable,
			"modelsKnown": account.ModelsKnown, "models": models,
			"origin": account.Origin,
		})
	}
	on := true
	if current := s.Store.Current(); current != nil {
		on = !current.Config.NoAccountDiscovery
	}
	respond.JSON(w, http.StatusOK, map[string]any{"status": snap.Status, "on": on, "accounts": rows})
	return http.StatusOK
}

func (s *Server) putAccountDiscovery(w http.ResponseWriter, r *http.Request) int {
	var body struct {
		On bool `json:"on"`
	}
	if err := decode(w, r, &body); err != nil {
		return badJSON(w)
	}
	err := s.Host.Update(func(cfg *state.Config) error {
		cfg.NoAccountDiscovery = !body.On
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	return s.listAccounts(w)
}

func (s *Server) postWorkBuddy(w http.ResponseWriter, r *http.Request) int {
	if s.Logins == nil {
		return invalidReason(w, "账号发现已关闭")
	}
	var body struct {
		Build   string `json:"build"`
		APIKey  string `json:"apiKey"`
		Import  string `json:"import"`
		Desktop bool   `json:"desktop"`
	}
	if err := decode(w, r, &body); err != nil {
		return badJSON(w)
	}
	machine := s.Logins()
	if machine.Off {
		return invalidReason(w, "账号发现已关闭")
	}
	var err error
	switch {
	case strings.TrimSpace(body.APIKey) != "":
		err = login.SaveWorkBuddyKey(machine, body.Build, body.APIKey)
	case body.Desktop:
		err = login.ImportWorkBuddyDesktop(r.Context(), machine, body.Build)
	case strings.TrimSpace(body.Import) != "":
		err = login.ImportWorkBuddy(r.Context(), machine, body.Build, []byte(body.Import))
	default:
		return invalidReason(w, "没有填写")
	}
	if err != nil {
		return invalidReason(w, workBuddyReason(err))
	}
	return s.listAccounts(w)
}

func workBuddyReason(err error) string {
	switch {
	case errors.Is(err, login.ErrWorkBuddyEncrypted):
		return "桌面登录已加密，当作没有这份登录"
	case errors.Is(err, login.ErrWorkBuddyRefresh):
		return "登录刷新失败"
	case errors.Is(err, login.ErrWorkBuddyStore):
		return "没有登录记录的位置"
	case errors.Is(err, os.ErrNotExist):
		return "没有这份桌面登录"
	case errors.Is(err, os.ErrInvalid):
		return "登录记录读不出来，没有改写"
	default:
		return "这份内容不是 WorkBuddy 登录"
	}
}

func (s *Server) offerModels(cfg state.Config) []agent.Model {
	models := agent.Offers(cfg)
	if s.Logins == nil {
		return models
	}
	snap := login.Discover(s.Logins())
	if snap.Status != "ok" {
		return models
	}
	for _, account := range snap.Accounts {
		if account.Plugin || !account.ModelsKnown {
			continue
		}
		for _, model := range account.Models {
			name := model.Name
			if name == "" {
				name = model.ID
			}
			models = append(models, agent.Model{
				ID: login.CatalogID(account.ID, model.ID), Name: name, Bare: model.ID,
				Context: model.Context, Reasoning: model.Reasoning,
			})
		}
	}
	return models
}
