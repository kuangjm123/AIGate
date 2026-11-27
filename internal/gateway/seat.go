package gateway

import (
	"strings"

	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/state"
)

// seatCandidate is one place a request may be tried: one upstream key, or one
// subscription account. The strategy sees these as seats. ProviderID stays
// what usage calls the provider; Key is stable for the life of the process so
// rest, spent, speed, and session affinity land on the same seat.
type seatCandidate struct {
	Key        string
	ProviderID string
	Account    string
	Model      string
	Protocol   Protocol
	Provider   state.Provider
	KeyIndex   int
	Login      bool
	// Weight is a group member's weight for the weight strategy. Zero means
	// unset; the route's provider weight is used when this is zero.
	Weight int
	// TopMember names the top-level group member or route provider that owns
	// this seat. Weighted stickiness corrects only a move across members.
	TopMember string
	// Scopes are the group nodes above this seat, outermost first.
	Scopes []*planScope
	// Reasoning levels of a subscription model. Known false means the catalog
	// did not say; a known empty list is a model with no selector.
	ReasoningLevels      []string
	ReasoningLevelsKnown bool
	Levelless            bool
	ReasoningFixed       string
	ReasoningAllowed     []string
	ReasoningMap         map[string]string
	// ReasoningMember is the member reference the map was read from. A nested
	// group can replace TopMember with an outer reference after the map is set.
	ReasoningMember string
}

func providerSeatKey(providerID, key string) string {
	return state.ProviderSeatKey(providerID, key)
}

func loginSeatKey(account string) string {
	return state.LoginSeatKey(account)
}

// providerCandidates turns each open key of each listed provider into a seat.
// A provider with no key adds nothing; validation refuses such a provider, and
// a hand-edited file should not turn into a dial without a credential.
func providerCandidates(cfg state.Config, asked string, providerIDs []string) []seatCandidate {
	var out []seatCandidate
	for _, providerID := range providerIDs {
		provider, ok := cfg.Provider(providerID)
		if !ok {
			continue
		}
		model := upstreamModel(cfg, asked, providerID)
		for i, key := range provider.Keys() {
			out = append(out, seatCandidate{
				Key:        providerSeatKey(providerID, key),
				ProviderID: providerID,
				Model:      model,
				Provider:   provider,
				KeyIndex:   i,
				TopMember:  providerID,
			})
		}
	}
	return out
}

// providerModelCandidates lists every key of one provider for one explicit
// model. It is the leaf of a group member; the model is not translated through
// the caller's catalog spelling.
func providerModelCandidates(cfg state.Config, providerID, model string) []seatCandidate {
	provider, ok := cfg.Provider(providerID)
	if !ok {
		return nil
	}
	out := make([]seatCandidate, 0, len(provider.Keys()))
	for i, key := range provider.Keys() {
		out = append(out, seatCandidate{
			Key:        providerSeatKey(providerID, key),
			ProviderID: providerID,
			Model:      model,
			Provider:   provider,
			KeyIndex:   i,
			TopMember:  providerID,
		})
	}
	return out
}

// loginSeatCandidates lists the readable subscription accounts that offer
// model. An explicit login/<account>/<model> request names one account and
// does not depend on a model list that discovery did not read.
func loginSeatCandidates(snap login.Snapshot, model string, explicitAccount string) []seatCandidate {
	if snap.Status != "ok" {
		return nil
	}
	var out []seatCandidate
	for _, account := range snap.Accounts {
		if !account.Readable || account.Plugin {
			continue
		}
		if explicitAccount == "" {
			if !account.ModelsKnown {
				continue
			}
			found := false
			for _, item := range account.Models {
				if item.ID == model {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		} else if account.ID != explicitAccount {
			continue
		}
		out = append(out, loginSeatFor(account, model))
	}
	return out
}

func loginSeatCandidate(account, model string) seatCandidate {
	return seatCandidate{
		Key:        loginSeatKey(account),
		ProviderID: "login/" + account,
		Account:    account,
		Model:      model,
		Protocol:   loginProtocol(account),
		Login:      true,
		TopMember:  "login/" + account,
	}
}

func loginSeatFor(account login.Account, model string) seatCandidate {
	seat := loginSeatCandidate(account.ID, model)
	for _, item := range account.Models {
		if item.ID != model {
			continue
		}
		seat.ReasoningLevels = append([]string(nil), item.ReasoningEfforts...)
		seat.ReasoningLevelsKnown = item.ReasoningEffortsKnown || item.ReasoningEfforts != nil
		seat.Levelless = seat.ReasoningLevelsKnown && len(item.ReasoningEfforts) == 0
		break
	}
	return seat
}

func findLoginAccount(snap login.Snapshot, id string) (login.Account, bool) {
	for _, account := range snap.Accounts {
		if account.ID == id {
			return account, true
		}
	}
	return login.Account{}, false
}

func filterVisionCandidates(cfg state.Config, asked string, candidates []seatCandidate) []seatCandidate {
	out := make([]seatCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		name := candidate.Model
		if name == "" {
			name = asked
		}
		if candidate.Login || visionOpen(cfg, name, candidate.ProviderID) {
			out = append(out, candidate)
		}
	}
	return out
}

func hasLoginSeat(candidates []seatCandidate) bool {
	for _, candidate := range candidates {
		if candidate.Login {
			return true
		}
	}
	return false
}

// sameVendor reports whether two adjacent seats belong to the same upstream
// account family. A failure inside one provider's keys, or one WorkBuddy
// family's accounts, may try the next seat before the route's eligible list
// is consulted. Crossing vendors keeps the route's rule.
func sameVendor(a, b seatCandidate) bool {
	if a.Login != b.Login {
		return false
	}
	if a.Login {
		return loginVendor(a.Account) == loginVendor(b.Account)
	}
	return a.ProviderID == b.ProviderID
}

func loginVendor(account string) string {
	if at := strings.IndexByte(account, '#'); at > 0 {
		return account[:at]
	}
	return account
}
