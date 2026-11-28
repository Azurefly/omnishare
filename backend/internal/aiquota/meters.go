package aiquota

import (
	"math"
	"strings"
	"time"
)

func unit(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	return str(v)
}

func parseHTTP(id string, d, usage object) (Reading, error) {
	r := Reading{At: time.Now().Unix()}
	if d["error"] != nil || d["success"] == false || d["isValid"] == false {
		return r, errFormat
	}
	switch id {
	case "deepseek":
		for _, item := range rows(d["balance_infos"]) {
			o := obj(item)
			currency := str(o["currency"])
			if currency != "CNY" && currency != "USD" {
				continue
			}
			if _, valid := num(o["total_balance"]); !valid {
				continue
			}
			r.field("可用余额", currency, o["total_balance"])
			r.field("充值余额", currency, o["topped_up_balance"])
			r.field("赠送余额", currency, o["granted_balance"])
		}
	case "moonshot-cn", "moonshot-global":
		if code := d["code"]; code != nil {
			if n, ok := num(code); !ok || n != 0 {
				return r, errFormat
			}
		}
		if d["status"] == false {
			return r, errFormat
		}
		currency := "CNY"
		if id == "moonshot-global" {
			currency = "USD"
		}
		data := obj(d["data"])
		r.field("可用余额", currency, data["available_balance"])
		r.field("现金余额", currency, data["cash_balance"])
		r.field("代金券余额", currency, data["voucher_balance"])
	case "openrouter":
		data := obj(d["data"])
		if ceiling, exists := data["limit"]; exists && ceiling == nil {
			r.unlimited("密钥支出上限")
		} else {
			r.field("密钥剩余额度", "USD", data["limit_remaining"])
			r.field("密钥支出上限", "USD", ceiling)
		}
		r.field("本月已用", "USD", data["usage_monthly"])
		r.field("累计已用", "USD", data["usage"])
	case "gemini":
		for _, v := range rows(d["buckets"]) {
			b := obj(v)
			label := str(b["modelId"])
			if label == "" {
				label = "项目额度"
			}
			if kind := str(b["tokenType"]); shortText(kind, 24) {
				label += " · " + kind
			}
			if fraction, ok := num(b["remainingFraction"]); ok && fraction >= 0 && fraction <= 1 {
				r.value(label, "%", fraction*100, stamp(b["resetTime"]))
			} else if amount, ok := num(b["remainingAmount"]); ok && amount >= 0 {
				r.value(label, "配额单位", amount, stamp(b["resetTime"]))
			}
		}
	case "xai":
		if cents, ok := num(obj(d["total"])["val"]); ok {
			r.value("团队预付账面余额", "USD", cents/100, 0)
		}
	case "opencode":
		for _, w := range [][2]string{{"rolling", "滚动周期"}, {"weekly", "本周"}, {"monthly", "本月"}} {
			row := obj(obj(d["usage"])[w[0]])
			r.used(w[1], row["percent"], stamp(row["resetsAt"]))
		}
	case "glm-cn", "glm-global":
		if d["code"] != nil {
			n, ok := num(d["code"])
			if !ok || (n != 0 && n != 200) {
				return r, errFormat
			}
		}
		data := obj(d["data"])
		if data == nil {
			data = d
		}
		for _, v := range rows(data["limits"]) {
			o := obj(v)
			number, _ := num(o["number"])
			u, _ := num(o["unit"])
			label := cadence(int64(number) * map[int64]int64{1: 1440, 3: 60, 5: 1, 6: 10080}[int64(u)])
			switch str(o["type"]) {
			case "TOKENS_LIMIT", "CREDIT_LIMIT":
			case "TIME_LIMIT":
				label = "工具调用 · " + label
			default:
				continue
			}
			r.used(label, o["percentage"], stamp(o["nextResetTime"]))
		}
	case "minimax-cn", "minimax-global":
		if code := obj(d["base_resp"])["status_code"]; code != nil {
			if n, ok := num(code); !ok || n != 0 {
				return r, errFormat
			}
		}
		var shared object
		for _, v := range rows(d["model_remains"]) {
			o := obj(v)
			switch str(o["model_name"]) {
			case "general":
				shared = o
			case "MiniMax-M*":
				if shared == nil {
					shared = o
				}
			}
		}
		for _, w := range [][3]string{{"current_interval", "当前周期", "end_time"}, {"current_weekly", "本周", "weekly_end_time"}} {
			if p, ok := minimaxRemaining(shared, w[0]); ok {
				if w[0] == "current_weekly" && shared["weekly_boost_permille"] != nil {
					boost, valid := num(shared["weekly_boost_permille"])
					if !valid || boost <= 0 || boost > 10000 {
						continue
					}
					p *= boost / 1000
				}
				r.value(w[1], "%", p, stamp(shared[w[2]]))
			}
		}
	case "kimi":
		kimiMeters(&r, d)
	case "newapi":
		data := d
		if d["object"] != "token_usage" {
			data = obj(d["data"])
		}
		if d["code"] == false || data["object"] != "token_usage" || (data["unit"] != nil && data["unit"] != "quota") {
			return r, errFormat
		}
		unlimited, ok := data["unlimited_quota"].(bool)
		if !ok {
			return r, errFormat
		}
		if unlimited {
			r.unlimited("令牌额度")
		} else {
			r.field("令牌余额", "站点配额", data["total_available"])
		}
		acct := obj(data["user_quota"])
		if acct["unit"] == nil || acct["unit"] == "quota" {
			r.field("账户余额", "站点配额", acct["total_available"])
		}
	case "oneapi":
		limit, lok := num(d["hard_limit_usd"])
		used, uok := num(usage["total_usage"])
		if d["object"] != "billing_subscription" || usage["object"] != "list" || !lok || !uok || used < 0 || limit < 0 {
			return r, errFormat
		}
		if limit == 100_000_000 {
			r.value("已用额度", "站点单位", used/100, 0)
		} else {
			r.value("计费余额", "站点单位", limit-used/100, 0)
		}
	case "sub2api":
		sub2Meters(&r, d)
	case "balance":
		if d["is_active"] != false {
			r.field("账户余额", unit(d["unit"], unit(d["currency"], "站点单位")), d["balance"])
		}
	}
	if len(r.Meters) == 0 {
		return r, errFormat
	}
	return r, nil
}

func minimaxRemaining(o object, prefix string) (float64, bool) {
	if state := o[prefix+"_status"]; state != nil {
		n, ok := num(state)
		if !ok || (n != 1 && n != 2) {
			return 0, false
		}
	}
	if v := o[prefix+"_remaining_percent"]; v != nil {
		n, ok := num(v)
		return n, ok && n >= 0 && n <= 100
	}
	total, tok := num(o[prefix+"_total_count"])
	left, lok := num(o[prefix+"_usage_count"])
	if !tok || !lok || total <= 0 || left < 0 || left > total {
		return 0, false
	}
	return left / total * 100, true
}

func kimiMeters(r *Reading, d object) {
	for _, w := range [][2]string{{"limit_5h", "5 小时"}, {"limit_7d", "7 天"}, {"limit_month_total", "本月总额度"}, {"limit_month_code", "本月编程"}} {
		o := obj(obj(d["usages"])[w[0]])
		if n, ok := num(o["used_ratio"]); ok && n >= 0 && n <= 1 {
			r.value(w[1], "%", (1-n)*100, stamp(o["reset_time"]))
		}
	}
	if len(r.Meters) > 0 {
		return
	}
	add := func(label string, o object) {
		limit, ok := num(o["limit"])
		if !ok || limit <= 0 {
			return
		}
		left, ok := num(o["remaining"])
		if !ok {
			used, valid := num(o["used"])
			if !valid {
				return
			}
			left = limit - used
		}
		if left >= 0 && left <= limit {
			r.value(label, "%", left/limit*100, stamp(o["resetTime"]))
		}
	}
	add("7 天", obj(d["usage"]))
	for _, v := range rows(d["limits"]) {
		o := obj(v)
		w := obj(o["window"])
		duration, _ := num(w["duration"])
		factor := map[string]float64{"TIME_UNIT_MINUTE": 1, "TIME_UNIT_HOUR": 60, "TIME_UNIT_DAY": 1440, "TIME_UNIT_SECOND": 1.0 / 60}[str(w["timeUnit"])]
		label := cadence(int64(duration * factor))
		if len(r.Meters) > 0 && r.Meters[0].Label == label {
			continue
		}
		add(label, obj(o["detail"]))
	}
}

func sub2Meters(r *Reading, d object) {
	quota := obj(d["quota"])
	mode := str(d["mode"])
	if mode == "" {
		currency := unit(d["unit"], unit(quota["unit"], "USD"))
		if v, ok := num(d["remaining"]); ok && v >= 0 {
			r.value("令牌余额", currency, v, 0)
			return
		}
		limit, lok := num(quota["limit"])
		used, uok := num(quota["used"])
		if lok && uok && limit > 0 && used >= 0 {
			r.value("令牌余额", currency, limit-used, 0)
		}
		return
	}
	if unit(d["unit"], "USD") != "USD" {
		return
	}
	switch mode {
	case "quota_limited":
		if quota["unit"] == "USD" {
			r.field("令牌总余额", "USD", quota["remaining"])
		}
		for _, v := range rows(d["rate_limits"]) {
			o := obj(v)
			label := map[string]string{"5h": "5 小时", "1d": "每日", "7d": "每周"}[str(o["window"])]
			n, ok := num(o["remaining"])
			if ok && label != "" && unit(o["unit"], "USD") == "USD" {
				r.value(label, "USD", n, stamp(o["reset_at"]))
			}
		}
		if len(r.Meters) == 0 {
			if n, ok := num(d["remaining"]); ok && n >= 0 {
				r.value("令牌余额", "USD", n, 0)
			}
		}
	case "unrestricted":
		sub := obj(d["subscription"])
		if sub == nil {
			r.field("账户余额", "USD", d["balance"])
			return
		}
		for _, w := range [][2]string{{"daily", "每日"}, {"weekly", "每周"}, {"monthly", "每月"}} {
			limit, ok := num(sub[w[0]+"_limit_usd"])
			used, valid := num(sub[w[0]+"_usage_usd"])
			if ok && valid && limit > 0 && used >= 0 {
				r.value(w[1], "USD", math.Max(0, limit-used), 0)
			}
		}
		if n, ok := num(d["remaining"]); len(r.Meters) == 0 && ok && n == -1 {
			r.unlimited("订阅额度")
		}
	}
}

func classifyCLIError(msg string) error {
	switch {
	case strings.Contains(strings.ToLower(msg), "auth"), strings.Contains(strings.ToLower(msg), "login"), strings.Contains(strings.ToLower(msg), "sign in"):
		return errLogin
	}
	return errFormat
}
