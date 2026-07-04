package main

// Summary 是一次评测的汇总指标。
type Summary struct {
	Total         int
	Passed        int     // 状态匹配 且 (无路由期望 或 路由匹配)
	RouteExpected int     // 有 expect_route 的用例数
	RouteMatched  int
	StatusMatched int
	RouteAcc      float64 // RouteMatched / RouteExpected
	SuccessRate   float64 // StatusMatched / Total
	AvgLatencyMs  float64
}

// computeSummary 从逐用例结果聚合出汇总指标。
func computeSummary(results []Result) Summary {
	var s Summary
	s.Total = len(results)
	var latSum int64
	for _, r := range results {
		statusMatch := r.statusMatched()
		routeConsidered := r.ExpectRoute != ""
		routeMatch := routeConsidered && r.ActualRoute == r.ExpectRoute

		if routeConsidered {
			s.RouteExpected++
			if routeMatch {
				s.RouteMatched++
			}
		}
		if statusMatch {
			s.StatusMatched++
		}
		if statusMatch && (!routeConsidered || routeMatch) {
			s.Passed++
		}
		latSum += r.LatencyMs
	}
	if s.RouteExpected > 0 {
		s.RouteAcc = float64(s.RouteMatched) / float64(s.RouteExpected)
	}
	if s.Total > 0 {
		s.SuccessRate = float64(s.StatusMatched) / float64(s.Total)
		s.AvgLatencyMs = float64(latSum) / float64(s.Total)
	}
	return s
}
