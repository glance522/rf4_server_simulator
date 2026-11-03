package gameserver

import (
	_ "embed"
	"encoding/json"
	"math"
)

//go:embed assets/fish_body_lengths.json
var fishBodyJSON []byte

type fishBodyReference struct {
	K          float64 `json:"k"`
	ReferenceG float64 `json:"reference_g"`
	ReferenceM float64 `json:"reference_m"`
}

var fishBodyReferences = func() map[string]fishBodyReference {
	var values map[string]fishBodyReference
	if err := json.Unmarshal(fishBodyJSON, &values); err != nil {
		panic(err)
	}
	return values
}()

// 仅替换通用兜底体长，保留已有鱼种专属系数；不重写历史鱼获。
func fishBodyLength(species string, weightG, k, p float64) float64 {
	if ref, ok := fishBodyReferences[species]; ok && math.Abs(k-0.0355) < 1e-9 && math.Abs(p-1.0/3) < 1e-9 {
		k = ref.K
	}
	return math.Round(k*math.Pow(weightG, p)*100000) / 100000
}
