package gameserver

import (
	"context"
	"math"
	"testing"
)

// lengthAnchor 是一条官服实测的「重量(g) -> 体长(m)」记录。
type lengthAnchor struct {
	weight int64
	want   float64
}

// 体长系数校准：同一重量下，狗鱼（细长）必须比鲤（短厚）显示得更长；
// 已有官方标定曲线和未列入校准表的鱼种必须保持原值。
func TestCalibratedLengthKP(t *testing.T) {
	pikeK, pikeP := calibratedLengthKP("n.pike", defaultSpeciesLengthK, defaultSpeciesLengthP)
	if math.Abs(pikeK-0.065519) > 1e-9 || math.Abs(pikeP-defaultSpeciesLengthP) > 1e-12 {
		t.Fatalf("n.pike 未应用校准: k=%v p=%v", pikeK, pikeP)
	}
	carpK, carpP := calibratedLengthKP("carp", defaultSpeciesLengthK, defaultSpeciesLengthP)
	if math.Abs(carpK-0.044208) > 1e-9 || math.Abs(carpP-defaultSpeciesLengthP) > 1e-12 {
		t.Fatalf("carp 未应用校准: k=%v p=%v", carpK, carpP)
	}
	if pikeK <= carpK {
		t.Fatalf("狗鱼系数应大于鲤: pike=%v carp=%v", pikeK, carpK)
	}
	if got := pikeK * math.Pow(5000, pikeP); got < 0.9 || got > 1.3 {
		t.Fatalf("5kg 狗鱼长度=%.2fm 偏离现实区间", got)
	}
	if got := carpK * math.Pow(5000, carpP); got < 0.55 || got > 0.85 {
		t.Fatalf("5kg 鲤长度=%.2fm 偏离现实区间", got)
	}

	// 静态库里的非默认 k/p 是官方样本标定曲线，不能被现实体型校准覆盖。
	official := []struct {
		key string
		k   float64
		p   float64
	}{
		{"perch", 0.021948, 0.40868},
		{"c.roach", 0.033498, 0.34010},
		{"s.bream", 0.066818, 0.20866},
		{"crucian", 0.035129, 1.0 / 3.0},
	}
	for _, c := range official {
		if gotK, gotP := calibratedLengthKP(c.key, c.k, c.p); gotK != c.k || gotP != c.p {
			t.Fatalf("%s 官方曲线被覆盖: got=(%v,%v) want=(%v,%v)", c.key, gotK, gotP, c.k, c.p)
		}
	}

	// 鲟鱼 / 鳇鱼 / 海图大型鱼的官服实测「体重 -> 体长」锚点。
	// 官服同一尾鱼的体重和体长线性相关，所以这 28 个鱼种走 speciesLengthLinear
	// (L = a + b·W_kg)。容差按 docs/gameplay.md 表中该鱼种的实测最大残差设定（+0.1cm 余量），
	// 唯一放宽的是拉多加鲟 12.5cm：官服两个点本身反向，见该鱼种条目注释。
	// k/p 仍保留为回退值（多数为 p=0.5371 共同指数）。
	for _, c := range []struct {
		key  string
		k, p float64
		tol  float64 // 允许偏差(m)
		want []lengthAnchor
	}{
		{"beluga", 0.003876, 0.5371, 0.007, []lengthAnchor{
			{weight: 1127143, want: 6.92},
			{weight: 610890, want: 4.94},
			{weight: 545912, want: 4.68},
		}},
		{"kaluga", 0.003901, 0.5371, 0.002, []lengthAnchor{
			{weight: 489987, want: 4.43},
			{weight: 511425, want: 4.55},
		}},
		{"ch_beluga", 0.003821, 0.5371, 0.014, []lengthAnchor{
			{weight: 521104, want: 4.58},
			{weight: 371828, want: 3.69},
			{weight: 410415, want: 3.94},
		}},
		{"osetr_balt", 0.006285, 0.5371, 0.002, []lengthAnchor{
			{weight: 211588, want: 4.60},
			{weight: 245116, want: 4.88},
		}},
		{"osetr_ship", 0.005853, 0.5371, 0.002, []lengthAnchor{
			{weight: 57377, want: 2.17},
			{weight: 74583, want: 2.35},
		}},
		{"r_sturgeon", 0.006622, 0.5371, 0.002, []lengthAnchor{
			{weight: 51882, want: 2.24},
			{weight: 122603, want: 3.65},
			{weight: 56400, want: 2.33},
		}},
		{"p_sturgeon", 0.006227, 0.5371, 0.002, []lengthAnchor{
			{weight: 106463, want: 3.13},
			{weight: 104620, want: 3.09},
			{weight: 103734, want: 3.07},
		}},
		{"st_sturgeon", 0.006206, 0.5371, 0.005, []lengthAnchor{
			{weight: 51789, want: 2.12},
			{weight: 50028, want: 2.10},
			{weight: 60587, want: 2.26},
		}},
		{"sterlet", 0.006867, 0.5371, 0.003, []lengthAnchor{
			{weight: 10114, want: 0.97},
			{weight: 11841, want: 1.07},
			{weight: 9479, want: 0.93},
		}},
		{"l_sturgeon", 0.006161, 0.5371, 0.125, []lengthAnchor{
			{weight: 64593, want: 2.19},
			{weight: 56733, want: 2.31},
			{weight: 46785, want: 2.04},
		}},
		{"x_gladius", 0.004545, 0.5371, 0.035, []lengthAnchor{
			{weight: 473093, want: 5.09},
			{weight: 507495, want: 5.23},
			{weight: 347799, want: 4.32},
		}},
		{"thynnus_t", 0.003585, 0.5371, 0.081, []lengthAnchor{
			{weight: 288013, want: 2.98},
			{weight: 359516, want: 3.50},
			{weight: 537409, want: 4.35},
		}},
		{"somniosus_m", 0.003999, 0.5371, 0.002, []lengthAnchor{
			{weight: 695412, want: 5.48},
			{weight: 725732, want: 5.63},
		}},
		{"lampris_g", 0.002795, 0.5371, 0.002, []lengthAnchor{
			{weight: 148693, want: 1.66},
			{weight: 212498, want: 2.05},
		}},
		{"lamna_n", 0.005660, 0.5371, 0.003, []lengthAnchor{
			{weight: 141952, want: 3.32},
			{weight: 155023, want: 3.45},
			{weight: 142714, want: 3.33},
		}},
		{"cetorhinus_m", 0.004410, 0.5371, 0.006, []lengthAnchor{
			{weight: 1428002, want: 8.87},
			{weight: 2838839, want: 12.90},
			{weight: 2865588, want: 12.97},
			{weight: 1301199, want: 8.50},
		}},
		{"halibut_w", 0.005335, 0.5371, 0.003, []lengthAnchor{
			{weight: 150392, want: 3.21},
			{weight: 164420, want: 3.39},
			{weight: 155219, want: 3.27},
		}},
		{"taimen", 0.005594, 0.5371, 0.002, []lengthAnchor{
			{weight: 54289, want: 1.97},
			{weight: 51611, want: 1.93},
			{weight: 92622, want: 2.57},
			{weight: 90630, want: 2.54},
		}},
		// 廓里湖红点鲑：官服 22.7~35.1kg = 1.13~1.50m。旧立方律 k=0.021413 只给
		// 0.60m，模型只有官服的一半，玩家实测反馈「体长不对」。
		{"chartus_kuoriius", 0.001630, 0.652510, 0.025, []lengthAnchor{
			{weight: 22705, want: 1.13},
			{weight: 23608, want: 1.16},
			{weight: 26717, want: 1.28},
			{weight: 23336, want: 1.15},
			{weight: 35137, want: 1.50},
		}},
		// 三种红点鲑（北极 / 黄斑 / 花羔）。旧立方律把它们算成 0.61~0.65m，
		// 官服同一尾是 0.92~1.17m，模型只有六成大小。
		{"a_char", 0.009986, 0.489400, 0.006, []lengthAnchor{
			{weight: 13792, want: 1.06},
			{weight: 12681, want: 1.02},
			{weight: 11799, want: 0.98},
			{weight: 16893, want: 1.17},
		}},
		{"dr_char", 0.005803, 0.546200, 0.002, []lengthAnchor{
			{weight: 10634, want: 0.92},
			{weight: 10905, want: 0.93},
			{weight: 15784, want: 1.14},
		}},
		{"malma", 0.012226, 0.478700, 0.014, []lengthAnchor{
			{weight: 7609, want: 0.88},
			{weight: 9882, want: 1.01},
			{weight: 10502, want: 1.02},
		}},
		// 莱文氏 / 白斑（库恩扎）/ 美洲红点鲑。旧参数分别是 0.029925 立方律、
		// 0.032491 立方律、默认 0.0355 立方律，一律把这三族算短。
		{"golets_lev", 0.012925, 0.481044, 0.004, []lengthAnchor{
			{weight: 1656, want: 0.46},
			{weight: 1613, want: 0.45},
			{weight: 1691, want: 0.46},
		}},
		{"kunja", 0.00009113, 0.992672, 0.034, []lengthAnchor{
			{weight: 12892, want: 1.08},
			{weight: 10647, want: 0.89},
			{weight: 11596, want: 1.02},
		}},
		{"salvel_l", 0.004837, 0.575668, 0.008, []lengthAnchor{
			{weight: 5048, want: 0.65},
			{weight: 5353, want: 0.68},
			{weight: 8939, want: 0.91},
			{weight: 5070, want: 0.66},
		}},
		// 内维 / 黑口 / 粗壮红点鲑，第三批。旧的 k=0.053634 / 0.048535 / 0.040936
		// 立方律分别把 1.7kg、4.5kg、7.6kg 算成 0.64 / 0.80 / 0.80m。
		{"neiva", 0.040604, 0.329462, 0.016, []lengthAnchor{
			{weight: 1652, want: 0.49},
			{weight: 244, want: 0.25},
			{weight: 1323, want: 0.41},
		}},
		{"palia_kr", 0.014075, 0.454245, 0.003, []lengthAnchor{
			{weight: 4051, want: 0.61},
			{weight: 4587, want: 0.65},
			{weight: 3185, want: 0.55},
		}},
		{"palia_lud", 0.015791, 0.442256, 0.008, []lengthAnchor{
			{weight: 5428, want: 0.68},
			{weight: 8958, want: 0.91},
			{weight: 1455, want: 0.40},
		}},
		// 大鳞麻哈鱼：玩家 2026-11-11 抄的官服四点（40.9~65.9kg = 1.46~1.63m），
		// 线性残差 ≤0.07cm。旧 k=0.029240 立方律在 45kg 只给 1.04m，短 45cm。
		{"chavycha", 0.029240, defaultSpeciesLengthP, 0.005, []lengthAnchor{
			{weight: 40875, want: 1.46},
			{weight: 43891, want: 1.48},
			{weight: 45174, want: 1.49},
			{weight: 65862, want: 1.63},
		}},
	} {
		gotK, gotP := calibratedLengthKP(c.key, defaultSpeciesLengthK, defaultSpeciesLengthP)
		if math.Abs(gotK-c.k) > 1e-6 || math.Abs(gotP-c.p) > 1e-9 {
			t.Fatalf("%s 体长参数未按官服实测修正: k=%v p=%v", c.key, gotK, gotP)
		}
		for _, a := range c.want {
			got := speciesLengthM(c.key, gotK, gotP, float64(a.weight))
			if math.Abs(got-a.want) > c.tol {
				t.Fatalf("%s 在 %.3fkg 时体长 %.3fm，官服实测 %.2fm（容差 %.2f）",
					c.key, float64(a.weight)/1000, got, a.want, c.tol)
			}
		}
	}

	// 2026-11 第四批：波罗的海、北极与北太平洋水域 23 个鱼种的官服成对实测点
	// （玩家在官服捕获记录里逐条抄的 体重/体长），外加图们江细鳞鲑的单点样本。
	// 这批鱼种此前全是默认立方律或按「站点最大重量/现实最大体长」粗锚的 k，
	// 普遍把鱼算短 15~50cm，所以和前面 28 个鱼种一样改走线性曲线。
	// 容差 5cm：单鱼种线性残差最大 3.55cm（帝王蟹），其余多在 1cm 内。
	for _, c := range []struct {
		key  string
		k    float64
		want []lengthAnchor
	}{
		{"rk_crab", 0.068525, []lengthAnchor{{12921, 1.60}, {9043, 1.45}, {7854, 1.34}}},
		{"conger", 0.073938, []lengthAnchor{{66166, 2.81}, {60320, 2.61}, {81559, 3.20}}},
		{"atl_cod", 0.046307, []lengthAnchor{{50464, 1.66}, {73936, 1.94}, {55661, 1.73}}},
		{"ambl_h", 0.046520, []lengthAnchor{{9177, 0.94}, {10545, 1.02}}},
		{"lophius_p", 0.050937, []lengthAnchor{{36187, 1.62}, {49191, 1.86}}},
		{"molva", 0.056831, []lengthAnchor{{25656, 1.62}, {43651, 1.98}}},
		{"centroloph_n", 0.045006, []lengthAnchor{{26003, 1.37}, {35464, 1.47}}},
		{"c_anguineus", 0.060554, []lengthAnchor{{57564, 2.19}, {34457, 1.64}, {70111, 2.50}}},
		{"polyprion", 0.051060, []lengthAnchor{{63694, 1.96}, {77854, 2.18}}},
		{"ambl_r", 0.052488, []lengthAnchor{{14709, 1.28}, {11509, 1.08}}},
		{"m_molva", 0.061958, []lengthAnchor{{30453, 1.81}, {46626, 2.23}}},
		{"alfonsino", 0.042449, []lengthAnchor{{10476, 0.92}, {9515, 0.88}}},
		{"meagre", 0.049336, []lengthAnchor{{58854, 1.83}, {91014, 2.21}}},
		{"m_zubatka", 0.053436, []lengthAnchor{{23083, 1.52}, {17394, 1.27}}},
		{"d_zubatka", 0.051260, []lengthAnchor{{16943, 1.30}, {18856, 1.36}}},
		{"p_zubatka", 0.049343, []lengthAnchor{{16171, 1.24}, {14507, 1.18}}},
		{"red_fish", 0.038384, []lengthAnchor{{3716, 0.58}, {4476, 0.63}}},
		{"sebastes_n", 0.043228, []lengthAnchor{{9544, 0.84}, {12542, 0.99}}},
		{"sebastes_m", 0.040735, []lengthAnchor{{5571, 0.68}, {7301, 0.79}}},
		{"mikija", 0.041885, []lengthAnchor{{9331, 0.88}, {8013, 0.80}}},
		{"onkor_m", 0.045437, []lengthAnchor{{6594, 0.76}, {8408, 0.92}}},
		{"onkor_mg", 0.040510, []lengthAnchor{{5577, 0.72}, {5081, 0.68}}},
		{"kijuch", 0.042865, []lengthAnchor{{5580, 0.71}, {6719, 0.81}}},
		{"lenok_t", 0.043336, []lengthAnchor{{7546, 0.85}}},
	} {
		gotK, gotP := calibratedLengthKP(c.key, defaultSpeciesLengthK, defaultSpeciesLengthP)
		if math.Abs(gotK-c.k) > 1e-6 || math.Abs(gotP-defaultSpeciesLengthP) > 1e-12 {
			t.Fatalf("%s 体长参数未按官服实测修正: k=%v p=%v", c.key, gotK, gotP)
		}
		for _, a := range c.want {
			if got := speciesLengthM(c.key, gotK, gotP, float64(a.weight)); math.Abs(got-a.want) > 0.05 {
				t.Fatalf("%s 在 %.3fkg 时体长 %.3fm，官服实测 %.2fm",
					c.key, float64(a.weight)/1000, got, a.want)
			}
		}
	}

	// 这批鱼种的实测点都在大体重段，普通渔获落在区间外，必须继续按立方根延拓：
	// 大西洋鳕 5kg 给 0.77m（线性外推会给 1.12m），海平鲉 1kg 给 0.36m（外推给 0.38m）。
	if got := speciesLengthM("atl_cod", 0.046307, defaultSpeciesLengthP, 5000); math.Abs(got-0.770) > 0.01 {
		t.Fatalf("大西洋鳕 5kg 应按端点立方根延拓到 0.770m: got=%.3fm", got)
	}
	if got := speciesLengthM("red_fish", 0.038384, defaultSpeciesLengthP, 1000); math.Abs(got-0.364) > 0.01 {
		t.Fatalf("海平鲉 1kg 应按端点立方根延拓到 0.364m: got=%.3fm", got)
	}

	// 线性表在实测区间外按立方根延拓，而不是线性外推：
	// 白斑红点鲑 1kg 用线性会给 0.11m（截距几乎为 0），延拓给 0.41m。
	// 这条也顺便确认三个新鱼种确实走到了 speciesLengthLinear 分支（幂律回退会差很多）。
	for _, c := range []struct {
		key    string
		k, p   float64
		weight float64
		want   float64
	}{
		{"kunja", 0.00009113, 0.992672, 1000, 0.413},
		{"golets_lev", 0.012925, 0.481044, 250, 0.242},
		{"salvel_l", 0.004837, 0.575668, 500, 0.304},
		{"palia_kr", 0.014075, 0.454245, 400, 0.275},
		{"palia_lud", 0.015791, 0.442256, 500, 0.282},
		// 本服大鳞麻哈鱼的采样区间（档位线 40/60kg ×1.25）常越过实测上限 65.9kg，
		// 上界外延必须走立方根：80kg 线性外推给 1.73m，与延拓结果只差 2mm，
		// 但 20kg 处线性外推给 1.32m（比 40.9kg 的 1.46m 只短 14cm），延拓给 1.15m 才合理。
		{"chavycha", 0.029240, defaultSpeciesLengthP, 20000, 1.151},
		{"chavycha", 0.029240, defaultSpeciesLengthP, 80000, 1.739},
	} {
		if got := speciesLengthM(c.key, c.k, c.p, c.weight); math.Abs(got-c.want) > 0.01 {
			t.Fatalf("%s 在 %.2fkg 时应按立方根延拓到 %.3fm: got=%.3fm",
				c.key, c.weight/1000, c.want, got)
		}
	}

	// 上界同样走延拓，不线性外推：本服蓝冠档位线（×1.25）会把大型鱼推过实测上限。
	// 黑海欧洲鳇实测上限 521.104kg=4.583m；1250kg 线性外推会给 8.90m，立方根延拓给 6.135m
	// （官服 521kg=4.58m 的走势才是参照）。这条锁定的是「上界外延不能退化成线性」。
	if got := speciesLengthM("ch_beluga", 0.003821, 0.5371, 1250000); math.Abs(got-6.135) > 0.02 {
		t.Fatalf("黑海欧洲鳇 1250kg 应按端点立方根延拓到 6.135m: got=%.3fm", got)
	}
	if got := speciesLengthM("ch_beluga", 0.003821, 0.5371, 1250000); got > 7.0 {
		t.Fatalf("黑海欧洲鳇 1250kg 不应线性外推到 8.9m: got=%.3fm", got)
	}
	// 上限内仍严格等于 a+b·W，确认上界守卫没把区间内的点也一起延拓。
	if got := speciesLengthM("ch_beluga", 0.003821, 0.5371, 521104); math.Abs(got-4.583) > 0.002 {
		t.Fatalf("黑海欧洲鳇 521.104kg（实测上限）应给 4.583m: got=%.3fm", got)
	}
	// 区间内必须严格等于 a+b·W，用来抓「守卫失效、悄悄回退到幂律」这类回归。
	// 期望值与幂律回退相差 1~35mm，容差 1e-3 足以区分。
	for _, c := range []struct {
		key    string
		k, p   float64
		weight float64
		want   float64
	}{
		{"neiva", 0.040604, 0.329462, 1323, 0.4246},
		{"palia_kr", 0.014075, 0.454245, 4051, 0.6112},
		{"palia_lud", 0.015791, 0.442256, 5428, 0.6734},
	} {
		if got := speciesLengthM(c.key, c.k, c.p, c.weight); math.Abs(got-c.want) > 1e-3 {
			t.Fatalf("%s 在区间内应走线性曲线 %.4f: got=%.4f", c.key, c.want, got)
		}
	}

	// 线性表之外（六须鲇 / 东西伯利亚鲟）必须继续走幂律。
	if got := speciesLengthM("w_catfish", 0.012744, 0.4769, 225992); math.Abs(got-4.56) > 0.02 {
		t.Fatalf("六须鲇应继续用幂律: got=%.2fm", got)
	}
	if got := speciesLengthM("carp", 0.044208, defaultSpeciesLengthP, 5000); math.Abs(got-0.75) > 0.03 {
		t.Fatalf("普通鲤应继续用立方律: got=%.2fm", got)
	}

	// 六须鲇与东西伯利亚鲟的官服实测数据显示体长不服从立方律：体重增长明显快于体长，
	// 大鱼更修长（K=体重/体长^3 从 3.5 降到 2.3），所以必须同时覆盖 k 与 p。
	// 锚点为官服实际钓获记录：
	//   欧洲六须鲇   105.934kg=3.18m / 129.663kg=3.49m / 225.992kg=4.56m
	//   东西伯利亚鲟 104.728kg=3.09m / 106.159kg=3.12m / 151.508kg=4.02m / 159.628kg=4.10m
	nonCubic := []struct {
		key    string
		k, p   float64
		weight float64 // g
		lo, hi float64 // 期望体长(m)
	}{
		{"w_catfish", 0.012744, 0.4769, 105934, 3.14, 3.22},
		{"w_catfish", 0.012744, 0.4769, 129663, 3.45, 3.53},
		{"w_catfish", 0.012744, 0.4769, 225992, 4.52, 4.60},
		{"zs_sturgeon", 0.001097, 0.6872, 104728, 3.05, 3.13},
		{"zs_sturgeon", 0.001097, 0.6872, 106159, 3.08, 3.16},
		{"zs_sturgeon", 0.001097, 0.6872, 151508, 3.94, 4.06},
		{"zs_sturgeon", 0.001097, 0.6872, 159628, 4.06, 4.14},
	}
	for _, c := range nonCubic {
		gotK, gotP := calibratedLengthKP(c.key, defaultSpeciesLengthK, defaultSpeciesLengthP)
		if math.Abs(gotK-c.k) > 1e-6 || math.Abs(gotP-c.p) > 1e-9 {
			t.Fatalf("%s 体长参数未按官服数据修正: k=%v p=%v", c.key, gotK, gotP)
		}
		if got := gotK * math.Pow(c.weight, gotP); got < c.lo || got > c.hi {
			t.Fatalf("%s 在 %.1fkg 时体长 %.2fm 偏离官服记录 [%.2f,%.2f]",
				c.key, c.weight/1000, got, c.lo, c.hi)
		}
	}

	// 未收录鱼种和非鱼类保持静态库原值。
	if gotK, gotP := calibratedLengthKP("not_a_fish", defaultSpeciesLengthK, defaultSpeciesLengthP); gotK != defaultSpeciesLengthK || gotP != defaultSpeciesLengthP {
		t.Fatalf("未校准鱼种被改写: k=%v p=%v", gotK, gotP)
	}
	if gotK, gotP := calibratedLengthKP("crawfish", defaultSpeciesLengthK, defaultSpeciesLengthP); gotK != defaultSpeciesLengthK || gotP != defaultSpeciesLengthP {
		t.Fatalf("非鱼类不应参与校准: k=%v p=%v", gotK, gotP)
	}
}

func TestCachedSpeciesLengthKPAppliesCalibration(t *testing.T) {
	s, db := newRigTestServer(t)
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO species_weight(fish_key,min_g,max_g,trophy_g,src,length_k,length_p,xp_c,xp_q,trophy_line_g,super_trophy_line_g,avg_g,samples)
		VALUES('carp',50,5000,2000,'test',0.0355,0.3333333333333333,0,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO species_weight(fish_key,min_g,max_g,trophy_g,src,length_k,length_p,xp_c,xp_q,trophy_line_g,super_trophy_line_g,avg_g,samples)
		VALUES('c.roach',80,900,318,'test',0.033498,0.34010,0,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []fishRollContext{{}, {cache: newFishingRollCache()}} {
		if k, p := s.cachedSpeciesLengthKP(ctx, c, "carp"); math.Abs(k-0.044208) > 1e-9 || math.Abs(p-defaultSpeciesLengthP) > 1e-12 {
			t.Fatalf("cached carp 未应用校准: k=%v p=%v", k, p)
		}
		if k, p := s.cachedSpeciesLengthKP(ctx, c, "c.roach"); k != 0.033498 || p != 0.34010 {
			t.Fatalf("cached 官曲线被覆盖: k=%v p=%v", k, p)
		}
	}
}
