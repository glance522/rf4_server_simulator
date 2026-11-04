package store

// 仅合并本体已确认共同成长的两组；其它 group_id 不据此推定共享。
var sharedSkillGroups = [...]struct {
	catID int64
	nodes [2]int64
}{
	{catID: 1, nodes: [2]int64{501, 503}},
	{catID: 2, nodes: [2]int64{702, 708}},
}

// SharedSkillNodeIDs 返回同一份技能投入对应的节点，不跨钓法大类合并。
func SharedSkillNodeIDs(catID, nodeID int64) []int64 {
	for _, group := range sharedSkillGroups {
		if group.catID == catID && (group.nodes[0] == nodeID || group.nodes[1] == nodeID) {
			return []int64{group.nodes[0], group.nodes[1]}
		}
	}
	return []int64{nodeID}
}

func sharedSkillPair(nodeID int64) ([2]int64, bool) {
	for _, group := range sharedSkillGroups {
		if group.nodes[0] == nodeID || group.nodes[1] == nodeID {
			return group.nodes, true
		}
	}
	return [2]int64{}, false
}

// ProjectSharedSkillNodes 保留原记录，以组内最高等级生成读取视图。
func ProjectSharedSkillNodes(nodes map[int64]int64) map[int64]int64 {
	out := make(map[int64]int64, len(nodes))
	for node, level := range nodes {
		out[node] = level
	}
	for _, group := range sharedSkillGroups {
		a, aOK := nodes[group.nodes[0]]
		b, bOK := nodes[group.nodes[1]]
		if aOK || bOK {
			level := max(int64(0), a, b)
			out[group.nodes[0]], out[group.nodes[1]] = level, level
		}
	}
	return out
}

// CountSkillNodePoints 同组等级只计一次，不包含装备临时赠级。
func CountSkillNodePoints(nodes map[int64]int64) int64 {
	var spent int64
	for node, level := range nodes {
		if _, shared := sharedSkillPair(node); !shared {
			spent += max(int64(0), level)
		}
	}
	for _, group := range sharedSkillGroups {
		spent += max(int64(0), nodes[group.nodes[0]], nodes[group.nodes[1]])
	}
	return spent
}
