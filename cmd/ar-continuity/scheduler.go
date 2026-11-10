package main

import (
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"time"
)

func affinityCandidate(q sdk.SchedulerPickRequest, r admissionRequest) (string, string) {
	if r.RawSession == "" {
		return "", ""
	}
	provider := q.Provider
	if provider == "" {
		provider = "mixed"
	}
	var lookup sdk.HostAffinityLookupResponse
	e := hostCall("host.affinity.lookup", map[string]string{"provider": provider, "model": q.Model, "session_id": r.RawSession}, &lookup)
	if e != nil || lookup.Status != "bound" {
		return "", ""
	}
	var runtime sdk.HostAuthGetRuntimeResponse
	if hostCall("host.auth.get_runtime", map[string]string{"auth_index": lookup.AuthIndex}, &runtime) != nil {
		return "", ""
	}
	return runtime.Auth.ID, runtime.Auth.Provider
}
func keyForCandidate(c sdk.SchedulerAuthCandidate) string { return c.Attributes["ar_key_hash"] }
func keySessions(s *routingLedger, key string) map[string]bool {
	sessions := map[string]bool{}
	for _, lease := range s.Leases {
		if lease.KeyHash == key {
			sessions[lease.Session] = true
		}
	}
	return sessions
}

func chooseWeighted(candidates []sdk.SchedulerAuthCandidate) sdk.SchedulerAuthCandidate {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	priority := candidates[0].Priority
	for _, c := range candidates {
		if c.Priority > priority {
			priority = c.Priority
		}
	}
	filtered := []sdk.SchedulerAuthCandidate{}
	weights := []int{}
	total := 0
	for _, c := range candidates {
		if c.Priority != priority {
			continue
		}
		w, e := strconv.Atoi(c.Attributes["weight"])
		if e != nil || w < 1 {
			w = 1
		}
		if w > 1000000 {
			w = 1000000
		}
		filtered = append(filtered, c)
		weights = append(weights, w)
		total += w
	}
	roll := rand.Intn(total)
	for i, w := range weights {
		roll -= w
		if roll < 0 {
			return filtered[i]
		}
	}
	return filtered[0]
}

// Keep provider priority/weight selection, then balance AR admissions by real
// Key rather than credential rows. Continuations never change the cursor.
func chooseAdmission(s *routingLedger, candidates []sdk.SchedulerAuthCandidate) sdk.SchedulerAuthCandidate {
	chosen := chooseWeighted(candidates)
	if chosen.Provider != pluginID {
		return chosen
	}
	groups := map[string][]sdk.SchedulerAuthCandidate{}
	for _, candidate := range candidates {
		if candidate.Provider == pluginID && candidate.Priority == chosen.Priority {
			key := keyForCandidate(candidate)
			groups[key] = append(groups[key], candidate)
		}
	}
	keys := []string{}
	minimum := int(^uint(0) >> 1)
	for key := range groups {
		count := 0
		for _, lease := range s.Leases {
			if lease.KeyHash == key {
				count++
			}
		}
		if count < minimum {
			minimum = count
			keys = keys[:0]
		}
		if count == minimum {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	key := keys[0]
	for _, candidate := range keys {
		if candidate > s.LastKeyHash {
			key = candidate
			break
		}
	}
	return chooseWeighted(groups[key])
}

func rejectAdmission(reason string) sdk.SchedulerPickResponse {
	return sdk.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "ar_session_ineligible", RejectReason: reason}
}
func pickAdmission(q sdk.SchedulerPickRequest) sdk.SchedulerPickResponse {
	r, ok := schedulerAdmission(q)
	hasAR := false
	for _, c := range q.Candidates {
		if c.Provider == pluginID {
			hasAR = true
		}
	}
	if !hasAR {
		pinned := false
		id := http.Header(q.Options.Headers).Get(admissionHeader)
		if ok {
			if r.Session != "" {
				_ = withRoutingLedger(func(s *routingLedger) error {
					if origin, known := s.Origins[r.Session]; known && !origin.Foreign && origin.Provider == pluginID {
						pinned = true
						return nil
					}
					if _, known := s.Origins[r.Session]; !known && !r.ParentConflict {
						if parent, exists := s.Origins[r.ParentSession]; exists && !parent.Foreign && parent.Provider == pluginID {
							pinned = true
							return nil
						}
					}
					if !r.Transient {
						s.Origins[r.Session] = sessionOrigin{Provider: "external", Foreign: true}
					}
					delete(s.Leases, id)
					return nil
				})
			}
		}
		if pinned {
			return rejectAdmission("The AR Key bound to this conversation is not available for this model. This conversation cannot switch Keys or providers.")
		}
		return sdk.SchedulerPickResponse{}
	}
	id := http.Header(q.Options.Headers).Get(admissionHeader)
	preferred, provider := affinityCandidate(q, r)
	result := rejectAdmission("AR 无法确认此请求的会话来源；带历史的请求需要稳定会话标识或可验证的父会话。")
	e := withRoutingLedger(func(s *routingLedger) error {
		origin, known := s.Origins[r.Session]
		pinnedAR := known && !origin.Foreign && origin.Provider == pluginID
		if provider != "" && provider != pluginID && r.Session != "" && !pinnedAR && !r.Transient {
			origin = sessionOrigin{Provider: provider, AuthID: preferred, Foreign: true}
			s.Origins[r.Session] = origin
			known = true
		}
		inherited := false
		parentKnown := false
		if !known && r.ParentConflict {
			result = rejectAdmission("Conflicting or invalid parent session references; cannot safely inherit a Key.")
			return nil
		}
		if !known && r.ParentSession != "" {
			if parent, exists := s.Origins[r.ParentSession]; exists {
				parentKnown = true
				origin, known = parent, true
				pinnedAR = !parent.Foreign && parent.Provider == pluginID
				inherited = pinnedAR
				if pinnedAR && provider == pluginID && preferred != "" && preferred != parent.AuthID {
					// A host-observed existing child binding outranks inheritance.
					found := false
					for _, candidate := range q.Candidates {
						if candidate.Provider == pluginID && candidate.ID == preferred {
							origin = sessionOrigin{Provider: pluginID, AuthID: preferred, KeyHash: keyForCandidate(candidate)}
							found = true
							break
						}
					}
					if !found {
						result = rejectAdmission("The child's existing AR Key is unavailable; refusing to replace it with the parent's Key.")
						return nil
					}
				}
				// Existing child bindings always win. A new child of an external
				// session cannot bring that session's history into AR.
				s.Origins[r.Session] = origin
			}
		}
		// A retry after an earlier failed first turn can leave a foreign ledger
		// entry behind. Codex explicitly marks a genuinely new initialization;
		// permit that one clean first turn to replace the stale marker. A cold
		// resume, assistant history, tool result or encrypted item remains out.
		eligible := ok && r.ID != "" && r.Session != "" && ((known && !origin.Foreign && origin.Provider == pluginID) || (!known && r.Fresh) || (known && origin.Foreign && r.Fresh && r.ExplicitFresh && !parentKnown && r.ParentSession == "" && !r.ParentConflict))
		allowed := []sdk.SchedulerAuthCandidate{}
		for _, candidate := range q.Candidates {
			if pinnedAR && candidate.Provider != pluginID {
				continue
			}
			if candidate.Provider == pluginID {
				if !eligible {
					continue
				}
				if known && origin.KeyHash != "" && origin.KeyHash != keyForCandidate(candidate) {
					continue
				}
				if pinnedAR && origin.KeyHash == "" && candidate.ID != origin.AuthID {
					continue
				}
			}
			allowed = append(allowed, candidate)
		}
		if len(allowed) == 0 {
			if pinnedAR {
				result = rejectAdmission("The AR Key bound to this conversation is unavailable. Retry later; this conversation cannot switch Keys or providers.")
			}
			return nil
		}
		var chosen sdk.SchedulerAuthCandidate
		// Preserve existing affinity where eligible, before considering candidate priority.
		sticky := preferred
		if (sticky == "" || inherited) && known {
			sticky = origin.AuthID
		}
		for _, c := range allowed {
			if c.ID == sticky {
				chosen = c
				break
			}
		}
		if chosen.ID == "" {
			chosen = chooseAdmission(s, allowed)
		}
		if r.Session != "" {
			if chosen.Provider == pluginID {
				if !known || inherited || (origin.Foreign && r.ExplicitFresh) {
					if !r.Transient {
						s.Origins[r.Session] = sessionOrigin{Provider: pluginID, AuthID: chosen.ID, KeyHash: keyForCandidate(chosen)}
					}
					if !inherited {
						s.LastKeyHash = keyForCandidate(chosen)
					}
				} else if pinnedAR && origin.KeyHash == "" {
					origin.KeyHash = keyForCandidate(chosen)
					s.Origins[r.Session] = origin
				}
				s.Leases[id] = admissionLease{Session: r.Session, KeyHash: keyForCandidate(chosen), AuthID: chosen.ID, RequestID: id, Created: time.Now().UTC()}
			} else {
				if !r.Transient {
					s.Origins[r.Session] = sessionOrigin{Provider: chosen.Provider, AuthID: chosen.ID, Foreign: true}
				}
				delete(s.Leases, id)
			}
		}
		result = sdk.SchedulerPickResponse{Handled: true, AuthID: chosen.ID}
		return nil
	})
	if e != nil {
		// A broken origin ledger must never allow an old conversation into AR.
		others := []sdk.SchedulerAuthCandidate{}
		for _, c := range q.Candidates {
			if c.Provider != pluginID {
				others = append(others, c)
			}
		}
		if len(others) > 0 {
			return sdk.SchedulerPickResponse{Handled: true, AuthID: chooseWeighted(others).ID}
		}
		return rejectAdmission("AR session admission state unavailable")
	}
	return result
}
