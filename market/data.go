package market

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ✅ 优化2：Funding Rate 缓存机制（节省 95% API 调用）
// Binance Funding Rate 每 8 小时才更新一次，使用 1 小时缓存完全合理
type FundingRateCache struct {
	Rate      float64
	UpdatedAt time.Time
}

var (
	fundingRateMap sync.Map // map[string]*FundingRateCache
	frCacheTTL     = 1 * time.Hour
)

// Get 获取指定代币的市场数据（支持动态时间线选择）
// timeframes: 可选参数，指定需要获取的时间线列表，如 []string{"1m", "15m", "1h", "4h"}
// 如果为空或nil，默认使用 ["15m", "1h", "4h"]
func Get(symbol string, timeframes []string) (*Data, error) {
	var klines1m, klines3m, klines5m, klines15m, klines1h, klines4h, klines1d []Kline
	var err error
	// 标准化symbol
	symbol = Normalize(symbol)

	// 设置默认时间线（如果未指定）
	if len(timeframes) == 0 {
		timeframes = []string{"15m", "1h", "4h"}
		log.Printf("⚠️  %s 未指定时间线，使用默认值: %v", symbol, timeframes)
	}

	// 创建时间线查找映射（提高查找效率）
	tfMap := make(map[string]bool)
	for _, tf := range timeframes {
		tfMap[tf] = true
	}

	// 确定最短时间线（用于计算当前价格和指标）
	shortestTF := ""
	tfPriority := []string{"1m", "3m", "5m", "15m", "1h", "4h", "1d"}
	for _, tf := range tfPriority {
		if tfMap[tf] {
			shortestTF = tf
			break
		}
	}

	// 如果没有找到任何短期时间线，使用3m作为默认（兼容旧行为）
	if shortestTF == "" {
		shortestTF = "3m"
		log.Printf("⚠️  %s 未配置任何时间线，使用3m作为默认短期时间线", symbol)
	}

	// 获取短期K线数据（用于当前价格和指标计算）
	var shortKlines []Kline
	switch shortestTF {
	case "1m":
		klines1m, err = WSMonitorCli.GetCurrentKlines(symbol, "1m")
		if err != nil {
			return nil, fmt.Errorf("获取1分钟K线失败: %v", err)
		}
		shortKlines = klines1m
	case "3m":
		klines3m, err = WSMonitorCli.GetCurrentKlines(symbol, "3m")
		if err != nil {
			return nil, fmt.Errorf("获取3分钟K线失败: %v", err)
		}
		shortKlines = klines3m
	case "5m":
		klines5m, err = WSMonitorCli.GetCurrentKlines(symbol, "5m")
		if err != nil {
			return nil, fmt.Errorf("获取5分钟K线失败: %v", err)
		}
		shortKlines = klines5m
	default:
		// 如果最短时间线是15m或更长，也获取一个短期数据用于stale检测
		klines3m, err = WSMonitorCli.GetCurrentKlines(symbol, "3m")
		if err != nil {
			return nil, fmt.Errorf("获取3分钟K线失败: %v", err)
		}
		shortKlines = klines3m
	}

	// Data staleness detection: Prevent DOGEUSDT-style price freeze issues (PR #800)
	if isStaleData(shortKlines, symbol) {
		log.Printf("⚠️  WARNING: %s detected stale data (consecutive price freeze), skipping symbol", symbol)
		return nil, fmt.Errorf("%s data is stale, possible cache failure", symbol)
	}

	// 根据配置获取其他时间线数据
	if tfMap["15m"] && len(klines15m) == 0 {
		klines15m, err = WSMonitorCli.GetCurrentKlines(symbol, "15m")
		if err != nil {
			return nil, fmt.Errorf("获取15分钟K线失败: %v", err)
		}
	}

	if tfMap["1h"] && len(klines1h) == 0 {
		klines1h, err = WSMonitorCli.GetCurrentKlines(symbol, "1h")
		if err != nil {
			return nil, fmt.Errorf("获取1小时K线失败: %v", err)
		}
	}

	if tfMap["4h"] {
		klines4h, err = WSMonitorCli.GetCurrentKlines(symbol, "4h")
		if err != nil {
			return nil, fmt.Errorf("获取4小时K线失败: %v", err)
		}
		// P0修复：检查 4h 数据完整性（如果用户选择了4h）
		if len(klines4h) == 0 {
			log.Printf("⚠️  WARNING: %s 缺少 4h K线数据，无法进行多周期趋势确认", symbol)
			return nil, fmt.Errorf("%s 缺少 4h K线数据", symbol)
		}
	}

	if tfMap["1d"] {
		klines1d, err = WSMonitorCli.GetCurrentKlines(symbol, "1d")
		if err != nil {
			log.Printf("⚠️  WARNING: %s 获取日线K线失败: %v，将继续处理但缺少日线数据", symbol, err)
			klines1d = nil // 日线数据失败不影响整体流程
		}
	}

	// 计算当前指标 (基于最短时间线的最新数据)
	currentPrice := shortKlines[len(shortKlines)-1].Close
	currentEMA20 := calculateEMA(shortKlines, 20)
	currentMACD := calculateMACD(shortKlines)
	currentRSI7 := calculateRSI(shortKlines, 7)

	// 计算价格变化百分比（基于可用数据）
	priceChange1h := 0.0
	priceChange4h := 0.0

	// 1小时价格变化：优先使用1h数据，其次用短期数据推算
	if len(klines1h) >= 2 {
		price1hAgo := klines1h[len(klines1h)-2].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	} else if shortestTF == "3m" && len(shortKlines) >= 21 {
		// 20个3分钟K线 = 1小时
		price1hAgo := shortKlines[len(shortKlines)-21].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	}

	// 4小时价格变化：使用4h数据
	if len(klines4h) >= 2 {
		price4hAgo := klines4h[len(klines4h)-2].Close
		if price4hAgo > 0 {
			priceChange4h = ((currentPrice - price4hAgo) / price4hAgo) * 100
		}
	}

	// 获取OI数据
	oiData, err := getOpenInterestData(symbol)
	if err != nil {
		// OI失败不影响整体,使用默认值
		oiData = &OIData{Latest: 0, Average: 0, ActualPeriod: "N/A"}
	}

	// ⚡ 新增：增強 OI 數據（加入多空比 - 完全免費）
	// 這不會影響性能，因為 Binance API 無限制且快速
	if err := EnhanceOIData(symbol, oiData); err != nil {
		// 多空比獲取失敗不影響整體流程，只記錄警告
		log.Printf("⚠️  %s 獲取多空比數據失敗: %v", symbol, err)
	}

	// 获取Funding Rate
	fundingRate, _ := getFundingRate(symbol)

	// ✅ 条件性计算时间线数据（只计算用户选择的时间线）
	var intradayData *IntradayData
	var midTermData15m *MidTermData15m
	var midTermData1h *MidTermData1h
	var longerTermData *LongerTermData
	var dailyData *DailyData

	// 计算日内系列数据 (1m/3m/5m)
	if len(klines1m) > 0 {
		intradayData = calculateIntradaySeries(klines1m)
	} else if len(klines3m) > 0 {
		intradayData = calculateIntradaySeries(klines3m)
	} else if len(klines5m) > 0 {
		intradayData = calculateIntradaySeries(klines5m)
	}

	// 计算15分钟系列数据（如果用户选择了15m）
	if len(klines15m) > 0 {
		midTermData15m = calculateMidTermSeries15m(klines15m)
	}

	// 计算1小时系列数据（如果用户选择了1h）
	if len(klines1h) > 0 {
		midTermData1h = calculateMidTermSeries1h(klines1h)
	}

	// 计算长期数据 (4小时，如果用户选择了4h)
	if len(klines4h) > 0 {
		longerTermData = calculateLongerTermData(klines4h)
	}

	// 计算日线数据（如果用户选择了1d）
	if len(klines1d) > 0 {
		dailyData = calculateDailyData(klines1d)
	}

	return &Data{
		Symbol:            symbol,
		CurrentPrice:      currentPrice,
		PriceChange1h:     priceChange1h,
		PriceChange4h:     priceChange4h,
		CurrentEMA20:      currentEMA20,
		CurrentMACD:       currentMACD,
		CurrentRSI7:       currentRSI7,
		OpenInterest:      oiData,
		FundingRate:       fundingRate,
		IntradaySeries:    intradayData,
		MidTermSeries15m:  midTermData15m,
		MidTermSeries1h:   midTermData1h,
		LongerTermContext: longerTermData,
		DailyContext:      dailyData,
	}, nil
}

// calculateEMA 计算EMA
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return 0
	}

	// 计算SMA作为初始EMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	// 计算EMA
	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateMACD 计算MACD
func calculateMACD(klines []Kline) float64 {
	if len(klines) < 26 {
		return 0
	}

	// 计算12期和26期EMA
	ema12 := calculateEMA(klines, 12)
	ema26 := calculateEMA(klines, 26)

	// MACD = EMA12 - EMA26
	return ema12 - ema26
}

// calculateRSI 计算RSI
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	gains := 0.0
	losses := 0.0

	// 计算初始平均涨跌幅
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// 使用Wilder平滑方法计算后续RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR 计算ATR
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	// 计算初始ATR
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	// Wilder平滑
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// calculateIntradaySeries 计算日内系列数据
func calculateIntradaySeries(klines []Kline) *IntradayData {
	data := &IntradayData{
		MidPrices:   make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
		Volume:      make([]float64, 0, 10),
	}

	// 获取最近10个数据点
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)
		data.Volume = append(data.Volume, klines[i].Volume)

		// 计算每个点的EMA20
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		// 计算每个点的MACD
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}

		// 计算每个点的RSI
		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	// 计算3m ATR14
	data.ATR14 = calculateATR(klines, 14)

	return data
}

// calculateMidTermSeries15m 计算15分钟系列数据
func calculateMidTermSeries15m(klines []Kline) *MidTermData15m {
	data := &MidTermData15m{
		MidPrices:   make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
	}

	// 获取最近10个数据点
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)

		// 计算每个点的EMA20
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		// 计算每个点的MACD
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}

		// 计算每个点的RSI
		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	return data
}

// calculateMidTermSeries1h 计算1小时系列数据
func calculateMidTermSeries1h(klines []Kline) *MidTermData1h {
	data := &MidTermData1h{
		MidPrices:   make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
	}

	// 获取最近10个数据点
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)

		// 计算每个点的EMA20
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		// 计算每个点的MACD
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}

		// 计算每个点的RSI
		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	return data
}

// calculateLongerTermData 计算长期数据
func calculateLongerTermData(klines []Kline) *LongerTermData {
	data := &LongerTermData{
		MACDValues:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
	}

	// 计算EMA
	data.EMA20 = calculateEMA(klines, 20)
	data.EMA50 = calculateEMA(klines, 50)

	// 计算ATR
	data.ATR3 = calculateATR(klines, 3)
	data.ATR14 = calculateATR(klines, 14)

	// 计算成交量
	if len(klines) > 0 {
		data.CurrentVolume = klines[len(klines)-1].Volume
		// 计算平均成交量
		sum := 0.0
		for _, k := range klines {
			sum += k.Volume
		}
		data.AverageVolume = sum / float64(len(klines))
	}

	// 计算MACD和RSI序列
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	return data
}

// calculateDailyData 计算日线数据
func calculateDailyData(klines []Kline) *DailyData {
	data := &DailyData{
		MidPrices:   make([]float64, 0, 90),
		EMA20Values: make([]float64, 0, 90),
		EMA50Values: make([]float64, 0, 90),
		MACDValues:  make([]float64, 0, 90),
		RSI14Values: make([]float64, 0, 90),
		ATR14Values: make([]float64, 0, 90),
		Volume:      make([]float64, 0, 90),
	}

	// 获取全部数据点（最多90个）
	for i := 0; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)
		data.Volume = append(data.Volume, klines[i].Volume)

		// 计算每个点的EMA20
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		// 计算每个点的EMA50
		if i >= 49 {
			ema50 := calculateEMA(klines[:i+1], 50)
			data.EMA50Values = append(data.EMA50Values, ema50)
		}

		// 计算每个点的MACD
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}

		// 计算每个点的RSI14
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}

		// 计算每个点的ATR14
		if i >= 14 {
			atr14 := calculateATR(klines[:i+1], 14)
			data.ATR14Values = append(data.ATR14Values, atr14)
		}
	}

	return data
}

// getOpenInterestData 获取OI数据（优化：优先使用缓存）
func getOpenInterestData(symbol string) (*OIData, error) {
	// ✅ 修复：统一symbol格式（确保大小写一致）
	symbol = Normalize(symbol)

	// ✅ 优化1：优先使用 collectOISnapshots 的缓存数据（每15分钟更新）
	// 好处：节省 50% API 调用，数据新鲜度 < 15 分钟
	if WSMonitorCli != nil {
		history := WSMonitorCli.GetOIHistory(symbol)
		log.Printf("🔍 [OI缓存检查] Symbol: %s, WSMonitorCli存在: true, 历史数据点数: %d", symbol, len(history))
		if len(history) > 0 {
			// 使用最新的快照（最多 15 分钟前的数据）
			latest := history[len(history)-1]

			var change4h float64
			var actualPeriod string
			change4h, actualPeriod = WSMonitorCli.CalculateOIChange4h(symbol, latest.Value)

			log.Printf("✅ [OI缓存命中] Symbol: %s, 使用缓存数据, 数据点数: %d, ActualPeriod: %s", symbol, len(history), actualPeriod)
			return &OIData{
				Latest:       latest.Value,
				Average:      latest.Value * 0.999, // 近似平均值
				Change4h:     change4h,
				ActualPeriod: actualPeriod,
				Historical:   history,
			}, nil
		} else {
			log.Printf("⚠️  [OI缓存未命中] Symbol: %s, 历史数据为空，降级到API调用", symbol)
		}
	} else {
		log.Printf("⚠️  [OI缓存不可用] Symbol: %s, WSMonitorCli为nil", symbol)
	}

	// ⚠️ 降级：缓存不存在时才调用 API（仅冷启动或缓存失效）
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/openInterest?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
		Time         int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	oi, _ := strconv.ParseFloat(result.OpenInterest, 64)

	// 计算4小时变化率
	var change4h float64
	var actualPeriod string
	if WSMonitorCli != nil {
		change4h, actualPeriod = WSMonitorCli.CalculateOIChange4h(symbol, oi)
	} else {
		actualPeriod = "N/A"
	}

	// 获取历史数据
	var history []OISnapshot
	if WSMonitorCli != nil {
		history = WSMonitorCli.GetOIHistory(symbol)
	}

	return &OIData{
		Latest:       oi,
		Average:      oi * 0.999,
		Change4h:     change4h,
		ActualPeriod: actualPeriod,
		Historical:   history,
	}, nil
}

// getFundingRate 获取资金费率（优化：使用 1 小时缓存）
func getFundingRate(symbol string) (float64, error) {
	// ✅ 修复：统一symbol格式（确保大小写一致）
	symbol = Normalize(symbol)

	// ✅ 优化2：检查缓存（有效期 1 小时）
	// Funding Rate 每 8 小时才更新，1 小时缓存非常合理
	if cached, ok := fundingRateMap.Load(symbol); ok {
		cache := cached.(*FundingRateCache)
		if time.Since(cache.UpdatedAt) < frCacheTTL {
			// 缓存命中，直接返回
			return cache.Rate, nil
		}
	}

	// ⚠️ 缓存过期或不存在，调用 API
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/premiumIndex?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		InterestRate    string `json:"interestRate"`
		Time            int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	rate, _ := strconv.ParseFloat(result.LastFundingRate, 64)

	// ✅ 更新缓存
	fundingRateMap.Store(symbol, &FundingRateCache{
		Rate:      rate,
		UpdatedAt: time.Now(),
	})

	return rate, nil
}

// Format 格式化输出市场数据
func Format(data *Data) string {
	var sb strings.Builder

	// 使用动态精度格式化价格
	priceStr := formatPriceWithDynamicPrecision(data.CurrentPrice)
	sb.WriteString(fmt.Sprintf("current_price = %s, current_ema20 = %.3f, current_macd = %.3f, current_rsi (7 period) = %.3f\n\n",
		priceStr, data.CurrentEMA20, data.CurrentMACD, data.CurrentRSI7))

	sb.WriteString(fmt.Sprintf("In addition, here is the latest %s open interest and funding rate for perps:\n\n",
		data.Symbol))

	if data.OpenInterest != nil {
		// P0修复：输出OI变化率（用于AI验证"近4小时上升>+3%"）
		// 简化版：只添加单位标注，避免 AI 误读合约数量为开仓金额
		oiLatestStr := fmt.Sprintf("%.0f contracts", data.OpenInterest.Latest)
		oiAverageStr := fmt.Sprintf("%.0f contracts", data.OpenInterest.Average)

		// P0修复：根據實際時間段動態顯示
		var changeLabel string
		if data.OpenInterest.ActualPeriod == "N/A" {
			changeLabel = "Change(4h): N/A (insufficient data, system uptime < 15min)"
		} else if data.OpenInterest.ActualPeriod == "0m" {
			// ✅ 修复：只有1個數據點（剛啟動）
			changeLabel = "Change(4h): 0.00% [just started, need 2+ samples for trend calculation]"
		} else if data.OpenInterest.ActualPeriod == "4h" {
			// 完整 4 小時數據
			changeLabel = fmt.Sprintf("Change(4h): %.3f%%", data.OpenInterest.Change4h)
		} else {
			// 降級使用較短時間段
			changeLabel = fmt.Sprintf("Change(4h): %.3f%% [degraded: using %s data, system uptime < 4h]",
				data.OpenInterest.Change4h, data.OpenInterest.ActualPeriod)
		}

		sb.WriteString(fmt.Sprintf("Open Interest: Latest: %s | Average: %s | %s\n\n",
			oiLatestStr, oiAverageStr, changeLabel))

		// ⚡ 新增：輸出多空比數據（免費數據源：Binance Futures API）
		if data.OpenInterest.LongShortRatio > 0 {
			longPct := data.OpenInterest.LongShortRatio / (1 + data.OpenInterest.LongShortRatio) * 100
			shortPct := 100 - longPct
			sb.WriteString(fmt.Sprintf("Market Sentiment (Long/Short Ratio): %.2f (%.1f%% long vs %.1f%% short)\n",
				data.OpenInterest.LongShortRatio, longPct, shortPct))

			if data.OpenInterest.TopTraderLongShortRatio > 0 {
				sb.WriteString(fmt.Sprintf("Top Traders Positioning: %.2f", data.OpenInterest.TopTraderLongShortRatio))
				if data.OpenInterest.TopTraderLongShortRatio > 1.2 {
					sb.WriteString(" (top traders bullish)")
				} else if data.OpenInterest.TopTraderLongShortRatio < 0.8 {
					sb.WriteString(" (top traders bearish)")
				} else {
					sb.WriteString(" (neutral)")
				}
				sb.WriteString("\n")
			}

			if data.OpenInterest.Sentiment != "" {
				sentimentLabel := data.OpenInterest.Sentiment
				switch sentimentLabel {
				case "bullish":
					sentimentLabel = "Bullish (market favors longs)"
				case "bearish":
					sentimentLabel = "Bearish (market favors shorts)"
				case "neutral":
					sentimentLabel = "Neutral (balanced)"
				}
				sb.WriteString(fmt.Sprintf("Overall Sentiment: %s\n", sentimentLabel))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(fmt.Sprintf("Funding Rate: %.2e\n\n", data.FundingRate))

	if data.IntradaySeries != nil {
		sb.WriteString("Intraday series (3‑minute intervals, oldest → latest):\n\n")

		if len(data.IntradaySeries.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatFloatSlice(data.IntradaySeries.MidPrices)))
		}

		if len(data.IntradaySeries.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA indicators (20‑period): %s\n\n", formatFloatSlice(data.IntradaySeries.EMA20Values)))
		}

		if len(data.IntradaySeries.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.IntradaySeries.MACDValues)))
		}

		if len(data.IntradaySeries.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (7‑Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI7Values)))
		}

		if len(data.IntradaySeries.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI14Values)))
		}

		if len(data.IntradaySeries.Volume) > 0 {
			sb.WriteString(fmt.Sprintf("3m Trading Volume (USDT, reference only): %s\n\n", formatFloatSlice(data.IntradaySeries.Volume)))
		}

		sb.WriteString(fmt.Sprintf("3m ATR (14‑period): %.3f\n\n", data.IntradaySeries.ATR14))
	}

	if data.MidTermSeries15m != nil {
		sb.WriteString("Mid‑term series (15‑minute intervals, oldest → latest):\n\n")

		if len(data.MidTermSeries15m.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatFloatSlice(data.MidTermSeries15m.MidPrices)))
		}

		if len(data.MidTermSeries15m.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA indicators (20‑period): %s\n\n", formatFloatSlice(data.MidTermSeries15m.EMA20Values)))
		}

		if len(data.MidTermSeries15m.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.MidTermSeries15m.MACDValues)))
		}

		if len(data.MidTermSeries15m.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (7‑Period): %s\n\n", formatFloatSlice(data.MidTermSeries15m.RSI7Values)))
		}

		if len(data.MidTermSeries15m.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.MidTermSeries15m.RSI14Values)))
		}
	}

	if data.MidTermSeries1h != nil {
		sb.WriteString("Mid‑term series (1‑hour intervals, oldest → latest):\n\n")

		if len(data.MidTermSeries1h.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatFloatSlice(data.MidTermSeries1h.MidPrices)))
		}

		if len(data.MidTermSeries1h.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA indicators (20‑period): %s\n\n", formatFloatSlice(data.MidTermSeries1h.EMA20Values)))
		}

		if len(data.MidTermSeries1h.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.MidTermSeries1h.MACDValues)))
		}

		if len(data.MidTermSeries1h.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (7‑Period): %s\n\n", formatFloatSlice(data.MidTermSeries1h.RSI7Values)))
		}

		if len(data.MidTermSeries1h.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.MidTermSeries1h.RSI14Values)))
		}
	}

	if data.LongerTermContext != nil {
		sb.WriteString("Longer‑term context (4‑hour timeframe):\n\n")

		sb.WriteString(fmt.Sprintf("20‑Period EMA: %.3f vs. 50‑Period EMA: %.3f\n\n",
			data.LongerTermContext.EMA20, data.LongerTermContext.EMA50))

		sb.WriteString(fmt.Sprintf("3‑Period ATR: %.3f vs. 14‑Period ATR: %.3f\n\n",
			data.LongerTermContext.ATR3, data.LongerTermContext.ATR14))

		sb.WriteString(fmt.Sprintf("Current Volume: %.3f vs. Average Volume: %.3f\n\n",
			data.LongerTermContext.CurrentVolume, data.LongerTermContext.AverageVolume))

		if len(data.LongerTermContext.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.LongerTermContext.MACDValues)))
		}

		if len(data.LongerTermContext.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.LongerTermContext.RSI14Values)))
		}
	}

	if data.DailyContext != nil {
		sb.WriteString("Daily series (1‑day intervals, oldest → latest):\n\n")

		if len(data.DailyContext.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("Daily close prices: %s\n\n", formatFloatSlice(data.DailyContext.MidPrices)))
		}

		if len(data.DailyContext.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA indicators (20‑period): %s\n\n", formatFloatSlice(data.DailyContext.EMA20Values)))
		}

		if len(data.DailyContext.EMA50Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA indicators (50‑period): %s\n\n", formatFloatSlice(data.DailyContext.EMA50Values)))
		}

		if len(data.DailyContext.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.DailyContext.MACDValues)))
		}

		if len(data.DailyContext.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.DailyContext.RSI14Values)))
		}

		if len(data.DailyContext.ATR14Values) > 0 {
			sb.WriteString(fmt.Sprintf("ATR indicators (14‑period): %s\n\n", formatFloatSlice(data.DailyContext.ATR14Values)))
		}

		if len(data.DailyContext.Volume) > 0 {
			sb.WriteString(fmt.Sprintf("Daily trading volume (USDT): %s\n\n", formatFloatSlice(data.DailyContext.Volume)))
		}
	}

	return sb.String()
}

// formatPriceWithDynamicPrecision 根据价格区间动态选择精度
// 这样可以完美支持从超低价 meme coin (< 0.0001) 到 BTC/ETH 的所有币种
func formatPriceWithDynamicPrecision(price float64) string {
	switch {
	case price < 0.0001:
		// 超低价 meme coin: 1000SATS, 1000WHY, DOGS
		// 0.00002070 → "0.00002070" (8位小数)
		return fmt.Sprintf("%.8f", price)
	case price < 0.001:
		// 低价 meme coin: NEIRO, HMSTR, HOT, NOT
		// 0.00015060 → "0.000151" (6位小数)
		return fmt.Sprintf("%.6f", price)
	case price < 0.01:
		// 中低价币: PEPE, SHIB, MEME
		// 0.00556800 → "0.005568" (6位小数)
		return fmt.Sprintf("%.6f", price)
	case price < 1.0:
		// 低价币: ASTER, DOGE, ADA, TRX
		// 0.9954 → "0.9954" (4位小数)
		return fmt.Sprintf("%.4f", price)
	case price < 100:
		// 中价币: SOL, AVAX, LINK, MATIC
		// 23.4567 → "23.4567" (4位小数)
		return fmt.Sprintf("%.4f", price)
	default:
		// 高价币: BTC, ETH (节省 Token)
		// 45678.9123 → "45678.91" (2位小数)
		return fmt.Sprintf("%.2f", price)
	}
}

// formatFloatSlice 格式化float64切片为字符串（使用动态精度）
func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = formatPriceWithDynamicPrecision(v)
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}

// Normalize 标准化symbol,确保是USDT交易对
func Normalize(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		return symbol
	}
	return symbol + "USDT"
}

// parseFloat 解析float值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		return strconv.ParseFloat(val, 64)
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("unsupported type: %T", v)
	}
}

// isStaleData detects stale data (consecutive price freeze)
// Fix DOGEUSDT-style issue: consecutive N periods with completely unchanged prices indicate data source anomaly
func isStaleData(klines []Kline, symbol string) bool {
	if len(klines) < 5 {
		return false // Insufficient data to determine
	}

	// Detection threshold: 5 consecutive 3-minute periods with unchanged price (15 minutes without fluctuation)
	const stalePriceThreshold = 5
	const priceTolerancePct = 0.0001 // 0.01% fluctuation tolerance (avoid false positives)

	// Take the last stalePriceThreshold K-lines
	recentKlines := klines[len(klines)-stalePriceThreshold:]
	firstPrice := recentKlines[0].Close

	// Check if all prices are within tolerance
	for i := 1; i < len(recentKlines); i++ {
		priceDiff := math.Abs(recentKlines[i].Close-firstPrice) / firstPrice
		if priceDiff > priceTolerancePct {
			return false // Price fluctuation exists, data is normal
		}
	}

	// Additional check: MACD and volume
	// If price is unchanged but MACD/volume shows normal fluctuation, it might be a real market situation (extremely low volatility)
	// Check if volume is also 0 (data completely frozen)
	allVolumeZero := true
	for _, k := range recentKlines {
		if k.Volume > 0 {
			allVolumeZero = false
			break
		}
	}

	if allVolumeZero {
		log.Printf("⚠️  %s stale data confirmed: price freeze + zero volume", symbol)
		return true
	}

	// Price frozen but has volume: might be extremely low volatility market, allow but log warning
	log.Printf("⚠️  %s detected extreme price stability (no fluctuation for %d consecutive periods), but volume is normal", symbol, stalePriceThreshold)
	return false
}

// ========== Jane Street风格：信号质量与市场制度分析 ==========

// CalculateSignalQuality 计算信号质量评分（严格的多维度验证）
func CalculateSignalQuality(data *Data, direction string) *SignalQuality {
    sq := &SignalQuality{}

    // 方向："LONG" 或 "SHORT"
    isLong := direction == "LONG"

    // 1️⃣ 趋势一致性 (Trend Confluence) - 权重30%
    // EMA20 > EMA50 且收盘价 > EMA20 → 做多信号
    trendScore := 0.0
    if data.LongerTermContext != nil {
        ema20 := data.CurrentEMA20
        ema50 := data.LongerTermContext.EMA50
        price := data.CurrentPrice

        if isLong {
            // 做多：需要 price > EMA20 > EMA50
            if price > ema20 && ema20 > ema50 {
                trendScore = 85.0
            } else if price > ema20 {
                trendScore = 60.0
            } else if price > ema50 {
                trendScore = 40.0
            } else {
                trendScore = 0.0
            }
        } else {
            // 做空：需要 price < EMA20 < EMA50
            if price < ema20 && ema20 < ema50 {
                trendScore = 85.0
            } else if price < ema20 {
                trendScore = 60.0
            } else if price < ema50 {
                trendScore = 40.0
            } else {
                trendScore = 0.0
            }
        }
    }
    sq.TrendConfidence = trendScore

    // 2️⃣ 动量强度 (Momentum) - 权重25%
    // MACD > 0 且RSI 30-70 之间 → 有效信号
    momentumScore := 0.0
    rsi := data.CurrentRSI7
    macd := data.CurrentMACD

    if isLong {
        // 做多：MACD > 0 且 RSI 30-70（不超买）
        if macd > 0 && rsi >= 30 && rsi <= 70 {
            momentumScore = 75.0 + (rsi-30)/40*10 // 最高85
        } else if macd > 0 && rsi > 70 {
            momentumScore = 50.0 // RSI超买，减分
        } else if macd > 0 {
            momentumScore = 60.0
        }
    } else {
        // 做空：MACD < 0 且 RSI 30-70（不超卖）
        if macd < 0 && rsi >= 30 && rsi <= 70 {
            momentumScore = 75.0 + (70-rsi)/40*10 // 最高85
        } else if macd < 0 && rsi < 30 {
            momentumScore = 50.0 // RSI超卖，减分
        } else if macd < 0 {
            momentumScore = 60.0
        }
    }
    sq.MomentumStrength = momentumScore

    // 3️⃣ 成交量确认 (Volume Confirmation) - 权重20%
    // 价格上升时成交量上升 → 有效确认
    volumeScore := 0.0
    if data.IntradaySeries != nil && len(data.IntradaySeries.Volume) >= 2 {
        vol := data.IntradaySeries.Volume
        avgVol := 0.0
        for _, v := range vol[:len(vol)-1] {
            avgVol += v
        }
        avgVol /= float64(len(vol) - 1)

        currentVol := vol[len(vol)-1]
        volRatio := currentVol / avgVol

        if isLong && data.PriceChange1h > 0 && volRatio > 1.0 {
            volumeScore = 80.0 + math.Min(volRatio-1.0, 0.2)*100 // 上限100
        } else if isLong && data.PriceChange1h > 0 {
            volumeScore = 60.0
        } else if !isLong && data.PriceChange1h < 0 && volRatio > 1.0 {
            volumeScore = 80.0 + math.Min(volRatio-1.0, 0.2)*100
        } else if !isLong && data.PriceChange1h < 0 {
            volumeScore = 60.0
        } else {
            volumeScore = 30.0
        }
    }
    sq.VolumeConfirm = volumeScore

    // 4️⃣ OI对齐度 (OI Alignment) - 权重15%
    // OI增长且价格上升 → 强信号；OI下降且价格上升 → 弱信号
    oiScore := 0.0
    if data.OpenInterest != nil {
        oiChange := data.OpenInterest.Change4h // 4小时变化率

        if isLong && data.PriceChange4h > 0 && oiChange > 0.03 { // OI增长>3%
            oiScore = 85.0
        } else if isLong && data.PriceChange4h > 0 && oiChange > 0 {
            oiScore = 70.0
        } else if isLong && data.PriceChange4h > 0 && oiChange < 0 {
            oiScore = 40.0 // 价格涨但OI减少 → 弱信号
        } else if !isLong && data.PriceChange4h < 0 && oiChange > 0.03 {
            oiScore = 85.0
        } else if !isLong && data.PriceChange4h < 0 && oiChange > 0 {
            oiScore = 70.0
        } else if !isLong && data.PriceChange4h < 0 && oiChange < 0 {
            oiScore = 40.0
        } else {
            oiScore = 30.0
        }
    }
    sq.OIAlignment = oiScore

    // 5️⃣ 资金费率反向信号 (Funding Rate Reversal) - 权重10%
    // 费率过高时做空 / 费率过低时做多 → 高收益
    fundingScore := 0.0
    fr := data.FundingRate

    if isLong && fr < 0.0001 { // 费率低 → 做多机会
        fundingScore = 80.0
    } else if isLong && fr < 0.0005 {
        fundingScore = 60.0
    } else if isLong && fr > 0.001 { // 费率太高 → 有风险
        fundingScore = 20.0
    } else if !isLong && fr > 0.001 { // 费率高 → 做空机会
        fundingScore = 80.0
    } else if !isLong && fr > 0.0005 {
        fundingScore = 60.0
    } else if !isLong && fr < 0.0001 { // 费率太低 → 有风险
        fundingScore = 20.0
    } else {
        fundingScore = 50.0
    }
    sq.FundingRateSignal = fundingScore

    // 综合评分（加权平均）
    sq.OverallScore = (sq.TrendConfidence*0.30 +
        sq.MomentumStrength*0.25 +
        sq.VolumeConfirm*0.20 +
        sq.OIAlignment*0.15 +
        sq.FundingRateSignal*0.10)

    // 判决：只有分数≥75才能交易
    switch {
    case sq.OverallScore >= 85:
        sq.Verdict = "STRONG_BUY"
        if !isLong {
            sq.Verdict = "STRONG_SELL"
        }
    case sq.OverallScore >= 75:
        sq.Verdict = "BUY"
        if !isLong {
            sq.Verdict = "SELL"
        }
    case sq.OverallScore >= 60:
        sq.Verdict = "NEUTRAL"
    case sq.OverallScore >= 50:
        sq.Verdict = "AVOID"
    default:
        sq.Verdict = "AVOID"
    }

    return sq
}

// DetectMarketRegime 检测市场状态（改变策略参数的关键）
func DetectMarketRegime(data *Data) *MarketRegime {
    regime := &MarketRegime{}

    if data.LongerTermContext == nil || data.DailyContext == nil {
        regime.State = "UNKNOWN"
        regime.Confidence = 0.0
        return regime
    }

    // 计算短期和长期趋势
    shortTrendPcts := []float64{}
    if data.MidTermSeries1h != nil && len(data.MidTermSeries1h.MidPrices) >= 2 {
        for i := 1; i < len(data.MidTermSeries1h.MidPrices); i++ {
            pct := (data.MidTermSeries1h.MidPrices[i] - data.MidTermSeries1h.MidPrices[i-1]) / data.MidTermSeries1h.MidPrices[i-1]
            shortTrendPcts = append(shortTrendPcts, pct)
        }
    }

    longTrendPcts := []float64{}
    if data.DailyContext != nil && len(data.DailyContext.MidPrices) >= 2 {
        for i := 1; i < len(data.DailyContext.MidPrices); i++ {
            pct := (data.DailyContext.MidPrices[i] - data.DailyContext.MidPrices[i-1]) / data.DailyContext.MidPrices[i-1]
            longTrendPcts = append(longTrendPcts, pct)
        }
    }

    avgShortTrend := calculateAvg(shortTrendPcts)
    avgLongTrend := calculateAvg(longTrendPcts)
    stdShort := calculateStd(shortTrendPcts)
    stdLong := calculateStd(longTrendPcts)

    // 状态判断
    if avgLongTrend > 0.01 && avgShortTrend > 0 {
        regime.State = "TRENDING_UP"
        regime.TrendStrength = math.Min(avgShortTrend/0.02, 1.0)
        regime.RecommendedLeverage = 5
        regime.Confidence = 0.8
    } else if avgLongTrend < -0.01 && avgShortTrend < 0 {
        regime.State = "TRENDING_DOWN"
        regime.TrendStrength = math.Min(-avgShortTrend/0.02, 1.0)
        regime.RecommendedLeverage = 5
        regime.Confidence = 0.8
    } else if stdShort > 0.03 {
        regime.State = "VOLATILE"
        regime.RecommendedLeverage = 2
        regime.Confidence = 0.7
    } else if stdShort < 0.005 {
        regime.State = "RANGE_BOUND"
        regime.RecommendedLeverage = 1
        regime.Confidence = 0.6
    } else {
        regime.State = "NEUTRAL"
        regime.RecommendedLeverage = 3
        regime.Confidence = 0.5
    }

    // 波动率等级
    if stdShort > 0.04 {
        regime.VolatilityLevel = "EXTREME"
    } else if stdShort > 0.02 {
        regime.VolatilityLevel = "HIGH"
    } else if stdShort > 0.01 {
        regime.VolatilityLevel = "MEDIUM"
    } else {
        regime.VolatilityLevel = "LOW"
    }

    return regime
}

// DetectExtremeOI 检测OI极端位置（反向交易信号）
func DetectExtremeOI(symbol string, oiData *OIData, priceChange4h float64) *ExtremeOIPosition {
    extreme := &ExtremeOIPosition{
        IsExtreme: false,
        Type:      "NEUTRAL",
    }

    if oiData == nil || len(oiData.Historical) == 0 {
        return extreme
    }

    // 计算OI百分位数
    values := make([]float64, len(oiData.Historical))
    for i, snap := range oiData.Historical {
        values[i] = snap.Value
    }

    percentile := calculatePercentile(values, oiData.Latest)
    extreme.OIPercentile = percentile

    // 极端位置定义
    if percentile > 0.95 && priceChange4h > 0.05 {
        // OI处于历史高位，价格大涨 → 多头拥挤 → 反向做空机会
        extreme.IsExtreme = true
        extreme.Type = "TOP_EXTREME"
        extreme.ReverseSignal = true
        extreme.ReverseStrength = math.Min((percentile-0.95)/0.05, 1.0) // 最高100%
    } else if percentile < 0.05 && priceChange4h < -0.05 {
        // OI处于历史低位，价格大跌 → 空头拥挤 → 反向做多机会
        extreme.IsExtreme = true
        extreme.Type = "BOTTOM_EXTREME"
        extreme.ReverseSignal = true
        extreme.ReverseStrength = math.Min((0.05-percentile)/0.05, 1.0)
    }

    return extreme
}

// CalculateVolatilityMetrics 计算波动率指标
func CalculateVolatilityMetrics(data *Data) *VolatilityMetrics {
    vm := &VolatilityMetrics{}

    // ATR
    if data.LongerTermContext != nil {
        vm.ATR14 = data.LongerTermContext.ATR14
        vm.ATR20 = data.LongerTermContext.ATR3 // 使用3小时ATR作为参考
    }

    // 历史波动率
    if data.MidTermSeries1h != nil && len(data.MidTermSeries1h.MidPrices) >= 20 {
        vm.HistoricalVol20 = calculateHistoricalVolatility(data.MidTermSeries1h.MidPrices, 20)
    }
    if data.DailyContext != nil && len(data.DailyContext.MidPrices) >= 60 {
        vm.HistoricalVol60 = calculateHistoricalVolatility(data.DailyContext.MidPrices, 60)
    }

    // Bollinger Bands
    if data.MidTermSeries1h != nil && len(data.MidTermSeries1h.MidPrices) >= 20 {
        prices := data.MidTermSeries1h.MidPrices
        sma := calculateSMA(prices, 20)
        std := calculateStd(prices)
        upper := sma + 2*std
        lower := sma - 2*std
        vm.BollingerWidth = (upper - lower) / sma
        vm.BollingerPosition = (data.CurrentPrice - lower) / (upper - lower)
    }

    return vm
}

// 辅助函数
func calculateAvg(values []float64) float64 {
    if len(values) == 0 {
        return 0.0
    }
    sum := 0.0
    for _, v := range values {
        sum += v
    }
    return sum / float64(len(values))
}

func calculateStd(values []float64) float64 {
    if len(values) == 0 {
        return 0.0
    }
    avg := calculateAvg(values)
    sumSq := 0.0
    for _, v := range values {
        sumSq += (v - avg) * (v - avg)
    }
    return math.Sqrt(sumSq / float64(len(values)))
}

func calculatePercentile(values []float64, target float64) float64 {
    if len(values) == 0 {
        return 0.5
    }
    count := 0
    for _, v := range values {
        if v <= target {
            count++
        }
    }
    return float64(count) / float64(len(values))
}

func calculateHistoricalVolatility(prices []float64, period int) float64 {
    if len(prices) < period {
        return 0.0
    }
    returns := make([]float64, len(prices)-1)
    for i := 1; i < len(prices); i++ {
        returns[i-1] = math.Log(prices[i] / prices[i-1])
    }
    if len(returns) > period {
        returns = returns[len(returns)-period:]
    }
    return calculateStd(returns)
}

func calculateSMA(prices []float64, period int) float64 {
    if len(prices) < period {
        return 0.0
    }
    sum := 0.0
    for i := len(prices) - period; i < len(prices); i++ {
        sum += prices[i]
    }
    return sum / float64(period)
}

// ensure stdLong is referenced to avoid "declared and not used" compile error
_ = stdLong
