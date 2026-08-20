package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 数据源是 Open-Meteo 的公开接口：免费、无需密钥、返回 JSON。
//
// 刻意不走 fetch_url 抓网页再剥标签：天气页面改版一次解析就全错，而这里要的是
// 每半小时无人值守取一次的稳定结果。
// 两个接口地址是变量而非常量，只为让测试指向本地的桩服务：真去打 open-meteo 的测试
// 既慢又不可复现。运行时不改它们。
var (
	geocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	forecastURL = "https://api.open-meteo.com/v1/forecast"
)

// maxRespBytes 限制单次响应的读取量。对方是可信接口，但读取量仍要有上界。
const maxRespBytes = 256 * 1024

// Place 是一处解析出来的地点。城市名到经纬度的解析结果几乎不变，因此按地名缓存，
// 不必每次取天气都重解析一遍。
type Place struct {
	Name string // 展示用的地名，如「杭州 · 浙江省 · 中国」
	Lat  float64
	Lon  float64
}

// Report 是一次天气观测。
type Report struct {
	Place     string
	Condition string // 中文天气现象（由 WMO 代码翻译）
	TempC     float64
	FeelsC    float64
	Humidity  int
	WindKmh   float64
	// Fetched 是取到这份数据的本地时刻。过期判定用它而不是接口给的观测时间：
	// 我们要防的是「取不到新数据还在拿旧的当现在用」，那是取数这一侧的事。
	Fetched time.Time
	// Yesterday / Tomorrow 是昨天与明天的概要，与现况同一次请求带回（零额外调用）。
	// 旧缓存没有这两项，零值表示未知、不注入，下次刷新自动补上。
	Yesterday DayInfo `json:"Yesterday,omitzero"`
	Tomorrow  DayInfo `json:"Tomorrow,omitzero"`
}

// DayInfo 是一天的天气概要。Date 是当地日期（如 2026-08-21）：「明天」会在午夜
// 之后变成「今天」，渲染与预报理由的时效都要靠它判断，不能只信字段名。
type DayInfo struct {
	Date      string
	Condition string
	MinC      float64
	MaxC      float64
	// Seen 是这一天的预报（按「有没有降水」这个粒度）第一次被看到的本地时刻，
	// 刷新时从上一次观测延续。注入时据此标出「早就知道了」——预报每轮都在眼前，
	// 没有这个标记，模型会把昨天就看过的「明天有雨」当成新消息，一遍遍提醒带伞。
	// 旧缓存没有该字段，零值按「刚看到」处理。
	Seen time.Time `json:"Seen,omitzero"`
}

// carrySeen 决定新观测里明天预报的 Seen：与上一次观测是同一天、降水与否也没变，
// 就是同一条消息，沿用旧时刻；否则从现在算起。
func carrySeen(prev, cur DayInfo, prevOK bool, now time.Time) time.Time {
	if prevOK && prev.known() && cur.known() && prev.Date == cur.Date && isWet(prev.Condition) == isWet(cur.Condition) && !prev.Seen.IsZero() {
		return prev.Seen
	}
	return now
}

// known 报告这一天是否有数据。
func (d DayInfo) known() bool { return d.Condition != "" && d.Date != "" }

// geocode 把地名解析成经纬度。
func geocode(ctx context.Context, client *http.Client, name string) (Place, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Place{}, fmt.Errorf("没有填写城市")
	}
	q := url.Values{}
	q.Set("name", name)
	q.Set("count", "1")
	q.Set("language", "zh")
	q.Set("format", "json")

	var body struct {
		Results []struct {
			Name      string  `json:"name"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Country   string  `json:"country"`
			Admin1    string  `json:"admin1"`
		} `json:"results"`
	}
	if err := getJSON(ctx, client, geocodeURL+"?"+q.Encode(), &body); err != nil {
		return Place{}, fmt.Errorf("解析地址失败: %w", err)
	}
	if len(body.Results) == 0 {
		return Place{}, fmt.Errorf("找不到 %q 这个地方，换一个写法试试（如「杭州」「Hangzhou」）", name)
	}
	r := body.Results[0]
	parts := []string{r.Name}
	for _, s := range []string{r.Admin1, r.Country} {
		if s != "" && s != r.Name {
			parts = append(parts, s)
		}
	}
	return Place{Name: strings.Join(parts, " · "), Lat: r.Latitude, Lon: r.Longitude}, nil
}

// fetchCurrent 取一处地点此刻的天气。
func fetchCurrent(ctx context.Context, client *http.Client, p Place) (Report, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(p.Lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(p.Lon, 'f', 4, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m")
	// 昨天与明天的概要搭同一次请求：past_days=1 + forecast_days=2 使 daily 数组
	// 恰为「昨天、今天、明天」三天，不多打一次接口
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min")
	q.Set("past_days", "1")
	q.Set("forecast_days", "2")
	q.Set("timezone", "auto")

	var body struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			Humidity    float64 `json:"relative_humidity_2m"`
			Apparent    float64 `json:"apparent_temperature"`
			Code        int     `json:"weather_code"`
			Wind        float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Daily struct {
			Time []string  `json:"time"`
			Code []int     `json:"weather_code"`
			Max  []float64 `json:"temperature_2m_max"`
			Min  []float64 `json:"temperature_2m_min"`
		} `json:"daily"`
	}
	if err := getJSON(ctx, client, forecastURL+"?"+q.Encode(), &body); err != nil {
		return Report{}, fmt.Errorf("获取天气失败: %w", err)
	}
	c := body.Current
	rep := Report{
		Place:     p.Name,
		Condition: conditionOf(c.Code),
		TempC:     c.Temperature,
		FeelsC:    c.Apparent,
		Humidity:  int(c.Humidity + 0.5),
		WindKmh:   c.Wind,
		Fetched:   time.Now(),
	}
	// 形状不符（字段缺失、长度不齐）就整体放弃这两项：宁缺勿错
	d := body.Daily
	if len(d.Time) == 3 && len(d.Code) == 3 && len(d.Max) == 3 && len(d.Min) == 3 {
		rep.Yesterday = DayInfo{Date: d.Time[0], Condition: conditionOf(d.Code[0]), MinC: d.Min[0], MaxC: d.Max[0]}
		rep.Tomorrow = DayInfo{Date: d.Time[2], Condition: conditionOf(d.Code[2]), MinC: d.Min[2], MaxC: d.Max[2]}
	}
	return rep, nil
}

// getJSON 发一次 GET 并解析 JSON 响应。
func getJSON(ctx context.Context, client *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "wen-agent/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("接口返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("响应格式无法解析: %w", err)
	}
	return nil
}

// conditions 是 WMO 天气代码到中文说法的对照。取日常说话会用的词，
// 不用「毛毛雨」这类书面气象术语之外没人说的表述。
var conditions = map[int]string{
	0:  "晴",
	1:  "大致晴朗",
	2:  "多云",
	3:  "阴",
	45: "有雾",
	48: "雾凇",
	51: "小雨",
	53: "细雨",
	55: "细密的雨",
	56: "冻雨",
	57: "冻雨",
	61: "小雨",
	63: "中雨",
	65: "大雨",
	66: "冻雨",
	67: "大的冻雨",
	71: "小雪",
	73: "中雪",
	75: "大雪",
	77: "米雪",
	80: "阵雨",
	81: "阵雨",
	82: "大阵雨",
	85: "阵雪",
	86: "大阵雪",
	95: "雷阵雨",
	96: "雷阵雨伴冰雹",
	99: "雷阵雨伴冰雹",
}

// conditionOf 翻译 WMO 天气代码。未收录的代码不编造说法，回落到「天气不明」——
// 注入的内容宁可含糊，也不能是错的。
func conditionOf(code int) string {
	if s, ok := conditions[code]; ok {
		return s
	}
	return "天气不明"
}

// probe 完整走一遍「解析地名 → 取天气」，不经过任何缓存。
// 设置页的测试按钮用它：测的就是这个城市能不能解析、接口通不通。
func probe(ctx context.Context, client *http.Client, location string) (Place, Report, error) {
	place, err := geocode(ctx, client, location)
	if err != nil {
		return Place{}, Report{}, err
	}
	rep, err := fetchCurrent(ctx, client, place)
	if err != nil {
		return place, Report{}, err
	}
	return place, rep, nil
}
