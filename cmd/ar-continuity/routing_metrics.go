package main

func routingSnapshot() map[string]any {
	result := map[string]any{"policy": "ar-origin-only", "active_sessions": map[string]int{}, "state": "ready"}
	e := withRoutingLedger(func(s *routingLedger) error {
		perKey := map[string]map[string]bool{}
		for _, lease := range s.Leases {
			if perKey[lease.KeyHash] == nil {
				perKey[lease.KeyHash] = map[string]bool{}
			}
			perKey[lease.KeyHash][lease.Session] = true
		}
		counts := map[string]int{}
		for _, lease := range s.Leases {
			counts[lease.AuthID] = len(perKey[lease.KeyHash])
		}
		result["active_sessions"] = counts
		result["known_sessions"] = len(s.Origins)
		return nil
	})
	if e != nil {
		result["state"] = "unavailable"
	}
	return result
}
