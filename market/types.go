package market

import (
	"fmt"
	"time"
)

// Data 市场数据结构
type Data struct {
	Symbol            string
	CurrentPrice      float64
	PriceChange1h     float64 // 1小时价格变化百分比
	PriceChange4h     float64 // 4小时价格变化百分比
	CurrentEMA20      float64
	CurrentMACD       float64
	CurrentRSI7       float64
	OpenInterest      *OIData
	FundingRate       float64
	IntradaySeries    *IntradayData   // 3分钟数据 - 实时价格
	MidTermSeries15m  *MidTermData15m // 15分钟数据 - 短期趋势
	MidTermSeries1h   *MidTermData1h  // 1小时数据 - 中期趋势
	LongerTermContext *LongerTermData // 4小时数据 - 长期趋势
	DailyContext      *DailyData      // 日线数据 - 长期趋势和极端位置判断

	// ⚡ 新增：宏觀市場情緒（免費來源：Yahoo Finance API、Alpha Vantage）
	MarketSentiment *MarketSentiment // VIX 恐慌指數、美股狀態等

	// ⚡ Jane Street新增
	SignalQuality     *SignalQuality     `json:"-"` // 信号质量评分
	MarketRegime      *MarketRegime      `json:"-"` // 市场状态
	ExtremeOI         *ExtremeOIPosition `json:"-"` // OI极端位置
	VolatilityMetrics *VolatilityMetrics `json:"-"` // 波动率指标
}

// OIData Open Interest数据
type OIData struct {
	Latest       float64      // 当前持仓量
	Average      float64      // 平均持仓量
	Change4h     float64      // 4小时变化率（百分比），P0修复：用于AI验证"近4小时上升>+3%"
	ActualPeriod string       // P0修复：实际使用的时间段（例如 "4h", "2.5h", "N/A"）
	Historical   []OISnapshot // 历史数据（用于计算变化率）

	// ⚡ 新增：多空情緒數據（免費來源：Binance Futures API）
	LongShortRatio          float64 // 全市場多空持倉人數比（>1 表示多頭占優）
	TopTraderLongShortRatio float64 // 大戶多空持倉量比（>1 表示大戶做多）
	Sentiment               string  // 市場情緒簡化標籤："bullish", "bearish", "neutral"
}

// OISnapshot OI历史快照
type OISnapshot struct {
	Value     float64   // OI值
	Timestamp time.Time // 时间戳
}

// IntradayData 日内数据(3分钟间隔) - 主要用于获取实时价格
type IntradayData struct {
	MidPrices   []float64
	EMA20Values []float64
	MACDValues  []float64
	RSI7Values  []float64
	RSI14Values []float64
	Volume      []float64
	ATR14       float64
}

// MidTermData15m 15分钟时间框架数据 - 短期趋势过滤
type MidTermData15m struct {
	MidPrices   []float64
	EMA20Values []float64
	MACDValues  []float64
	RSI7Values  []float64
	RSI14Values []float64
}

// MidTermData1h 1小时时间框架数据 - 中期趋势确认
type MidTermData1h struct {
	MidPrices   []float64
	EMA20Values []float64
	MACDValues  []float64
	RSI7Values  []float64
	RSI14Values []float64
}

// LongerTermData 长期数据(4小时时间框架)
type LongerTermData struct {
	EMA20         float64
	EMA50         float64
	ATR3          float64
	ATR14         float64
	CurrentVolume float64
	AverageVolume float64
	MACDValues    []float64
	RSI14Values   []float64
}

// DailyData 日线数据 - 用于长期趋势判断和极端位置识别
type DailyData struct {
	MidPrices   []float64 // 日线收盘价序列
	EMA20Values []float64 // EMA20序列
	EMA50Values []float64 // EMA50序列
	MACDValues  []float64 // MACD序列
	RSI14Values []float64 // RSI14序列
	ATR14Values []float64 // ATR14序列（波动率）
	Volume      []float64 // 成交量序列
}

// Binance API 响应结构
type ExchangeInfo struct {
	Symbols []SymbolInfo `json:"symbols"`
}

type SymbolInfo struct {
	Symbol            string `json:"symbol"`
	Status            string `json:"status"`
	BaseAsset         string `json:"baseAsset"`
	QuoteAsset        string `json:"quoteAsset"`
	ContractType      string `json:"contractType"`
	PricePrecision    int    `json:"pricePrecision"`
	QuantityPrecision int    `json:"quantityPrecision"`
}

type Kline struct {
	OpenTime            int64   `json:"openTime"`
	Open                float64 `json:"open"`
	High                float64 `json:"high"`
	Low                 float64 `json:"low"`
	Close               float64 `json:"close"`
	Volume              float64 `json:"volume"`
	CloseTime           int64   `json:"closeTime"`
	QuoteVolume         float64 `json:"quoteVolume"`
	Trades              int     `json:"trades"`
	TakerBuyBaseVolume  float64 `json:"takerBuyBaseVolume"`
	TakerBuyQuoteVolume float64 `json:"takerBuyQuoteVolume"`
}

type KlineResponse []interface{}

// BinanceErrorResponse represents Binance API error response
type BinanceErrorResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// Error implements error interface
func (e *BinanceErrorResponse) Error() string {
	return fmt.Sprintf("Binance API error (code %d): %s", e.Code, e.Msg)
}

type PriceTicker struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

type Ticker struct {
	Symbol    string  `json:"symbol"`
	LastPrice float64 `json:"lastPrice"`
	Volume    float64 `json:"volume,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"`
}

type Ticker24hr struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
}

// 特征数据结构
type SymbolFeatures struct {
	Symbol           string    `json:"symbol"`
	Timestamp        time.Time `json:"timestamp"`
	Price            float64   `json:"price"`
	PriceChange15Min float64   `json:"price_change_15min"`
	PriceChange1H    float64   `json:"price_change_1h"`
	PriceChange4H    float64   `json:"price_change_4h"`
	Volume           float64   `json:"volume"`
	VolumeRatio5     float64   `json:"volume_ratio_5"`
	VolumeRatio20    float64   `json:"volume_ratio_20"`
	VolumeTrend      float64   `json:"volume_trend"`
	RSI14            float64   `json:"rsi_14"`
	SMA5             float64   `json:"sma_5"`
	SMA10            float64   `json:"sma_10"`
	SMA20            float64   `json:"sma_20"`
	HighLowRatio     float64   `json:"high_low_ratio"`
	Volatility20     float64   `json:"volatility_20"`
	PositionInRange  float64   `json:"position_in_range"`
}

// 警报数据结构
type Alert struct {
	Type      string    `json:"type"`
	Symbol    string    `json:"symbol"`
	Value     float64   `json:"value"`
	Threshold float64   `json:"threshold"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type Config struct {
	AlertThresholds AlertThresholds `json:"alert_thresholds"`
	UpdateInterval  int             `json:"update_interval"` // seconds
	CleanupConfig   CleanupConfig   `json:"cleanup_config"`
}

type AlertThresholds struct {
	VolumeSpike      float64 `json:"volume_spike"`
	PriceChange15Min float64 `json:"price_change_15min"`
	VolumeTrend      float64 `json:"volume_trend"`
	RSIOverbought    float64 `json:"rsi_overbought"`
	RSIOversold      float64 `json:"rsi_oversold"`
}
type CleanupConfig struct {
	InactiveTimeout   time.Duration `json:"inactive_timeout"`    // 不活跃超时时间
	MinScoreThreshold float64       `json:"min_score_threshold"` // 最低评分阈值
	NoAlertTimeout    time.Duration `json:"no_alert_timeout"`    // 无警报超时时间
	CheckInterval     time.Duration `json:"check_interval"`      // 检查间隔
}

var config = Config{
	AlertThresholds: AlertThresholds{
		VolumeSpike:      3.0,
		PriceChange15Min: 0.05,
		VolumeTrend:      2.0,
		RSIOverbought:    70,
		RSIOversold:      30,
	},
	CleanupConfig: CleanupConfig{
		InactiveTimeout:   30 * time.Minute,
		MinScoreThreshold: 15.0,
		NoAlertTimeout:    20 * time.Minute,
		CheckInterval:     5 * time.Minute,
	},
	UpdateInterval: 60, // 1 minute
}

// ========== Jane Street风格：信号质量与市场制度 ==========

// SignalQuality 信号质量评分（0-100，75+才能交易）
type SignalQuality struct {
	TrendConfidence   float64 `json:"trend_confidence"`   // 趋势一致性（EMA、价格、成交量）
	MomentumStrength  float64 `json:"momentum_strength"`  // 动量强度（MACD、RSI）
	VolumeConfirm     float64 `json:"volume_confirm"`     // 成交量确认度
	OIAlignment       float64 `json:"oi_alignment"`       // OI与价格对齐度
	FundingRateSignal float64 `json:"funding_rate_signal"` // 资金费率反向信号
	OverallScore      float64 `json:"overall_score"`      // 综合评分
	Verdict           string  `json:"verdict"`            // "STRONG_BUY", "BUY", "NEUTRAL", "SELL", "STRONG_SELL", "AVOID"
}

// MarketRegime 市场状态识别
type MarketRegime struct {
	State              string  `json:"state"`               // "TRENDING_UP", "TRENDING_DOWN", "RANGE_BOUND", "VOLATILE", "CAPITULATION", "EUPHORIA"
	Confidence         float64 `json:"confidence"`         // 状态确定度
	VolatilityLevel    string  `json:"volatility_level"`   // "LOW", "MEDIUM", "HIGH", "EXTREME"
	TrendStrength      float64 `json:"trend_strength"`     // 0-1，趋势强度
	SupportResistance  [2]float64 `json:"support_resistance"` // [support, resistance]
	RecommendedLeverage int    `json:"recommended_leverage"` // 动态杠杆建议
}

// ExtremeOIPosition OI极端位置检测
type ExtremeOIPosition struct {
	IsExtreme        bool    `json:"is_extreme"`        // 是否处于极端位置
	Type             string  `json:"type"`              // "TOP_EXTREME" (多头拥挤) / "BOTTOM_EXTREME" (空头拥挤) / "NEUTRAL"
	OIPercentile     float64 `json:"oi_percentile"`     // OI在历史中的百分位数
	ReverseSignal    bool    `json:"reverse_signal"`    // 是否应该反向交易
	ReverseStrength  float64 `json:"reverse_strength"`  // 反向信号强度（0-1）
}

// VolatilityMetrics 波动率指标
type VolatilityMetrics struct {
	ATR14             float64 `json:"atr_14"`           // Average True Range (14周期)
	ATR20             float64 `json:"atr_20"`           // Average True Range (20周期)
	HistoricalVol20   float64 `json:"historical_vol_20"` // 20周期历史波动率
	HistoricalVol60   float64 `json:"historical_vol_60"` // 60周期历史波动率
	BollingerWidth    float64 `json:"bollinger_width"`   // Bollinger Band宽度
	BollingerPosition float64 `json:"bollinger_position"` // 价格在BB中的位置 (0-1)
}

// TradingStats 交易统计（用于Sharpe调整）
type TradingStats struct {
	RecentWinRate     float64 `json:"recent_win_rate"`    // 最近20笔交易的胜率
	RecentAvgRR       float64 `json:"recent_avg_rr"`      // 最近20笔的平均风险回报比
	TradeFrequency1h  int     `json:"trade_frequency_1h"` // 过去1小时的交易次数
	TradeFrequency24h int     `json:"trade_frequency_24h"` // 过去24小时的交易次数
	MaxDrawdown       float64 `json:"max_drawdown"`       // 最大回撤
	SharpeRatio       float64 `json:"sharpe_ratio"`       // 当前Sharpe比率
	CumulativePnL     float64 `json:"cumulative_pnl"`     // 累计盈亏
}

// MarketSentiment 全局市场情绪（来自VIX、美股等）
type MarketSentiment struct {
	VIX              float64 `json:"vix"`               // 恐慌指数
	SPX              float64 `json:"spx"`               // 标普500指数
	SPXChange4h      float64 `json:"spx_change_4h"`    // 4小时变化
	USEquityTrend    string  `json:"us_equity_trend"`  // "BULLISH", "BEARISH", "NEUTRAL"
	CryptoSentiment  string  `json:"crypto_sentiment"` // "EUPHORIA", "BULLISH", "NEUTRAL", "BEARISH", "CAPITULATION"
	RiskOnOff        string  `json:"risk_on_off"`      // "RISK_ON", "RISK_OFF"
	FederalRateEnv   string  `json:"federal_rate_env"` // "LOOSE", "NEUTRAL", "TIGHT"
}
