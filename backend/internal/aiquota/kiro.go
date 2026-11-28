package aiquota

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var kiroVersion = regexp.MustCompile(`(?i)^(?:kiro(?:-cli)?\s+(?:version\s+)?v?)?(\d+)\.(\d+)\.(\d+)(?:\s|$)`)
var terminalColor = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
var creditPair = regexp.MustCompile(`(?i)(-?[0-9][0-9,]*(?:\.[0-9]+)?)\s*(?:/|of|out of)\s*(-?[0-9][0-9,]*(?:\.[0-9]+)?)`)
var creditNumber = regexp.MustCompile(`-?[0-9][0-9,]*(?:\.[0-9]+)?`)

func queryKiro(ctx context.Context) (Reading, error) {
	path, e := executable("kiro-cli")
	if e != nil {
		return Reading{}, e
	}
	ver := command(ctx, path, "--version")
	var buf cappedBuffer
	ver.Stdout = &buf
	if ver.Run() != nil {
		return Reading{}, errors.New("无法读取 Kiro CLI 版本")
	}
	m := kiroVersion.FindStringSubmatch(strings.TrimSpace(buf.String()))
	if len(m) == 0 {
		return Reading{}, errors.New("无法确认 Kiro CLI 版本")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 3 || major == 3 && minor < 1 {
		return Reading{}, errors.New("请更新至 Kiro CLI 3.1 或更高版本后查询额度")
	}
	c := command(ctx, path, "chat", "--no-interactive", "/usage")
	c.Env = append(os.Environ(), "NO_COLOR=1")
	buf.Reset()
	c.Stdout = &buf
	if c.Run() != nil {
		return Reading{}, errors.New("Kiro CLI 未能返回额度，请确认已登录且 /usage 可用")
	}
	return parseKiroReport(buf.String())
}
func parseKiroReport(s string) (Reading, error) {
	r := Reading{At: time.Now().Unix()}
	for _, line := range strings.Split(terminalColor.ReplaceAllString(s, ""), "\n") {
		text := strings.ToLower(strings.TrimSpace(line))
		if len(text) > 240 || !strings.Contains(text, "credit") || strings.Contains(text, "%") && !creditPair.MatchString(text) {
			continue
		}
		// Session spend and the account's credit pool use different units of
		// scope. Parse only explicit labels, never the first number on screen.
		prefix := "账户"
		switch {
		case strings.Contains(text, "session"):
			prefix = "本次会话"
		case strings.Contains(text, "bonus"):
			prefix = "奖励"
		case strings.Contains(text, "trial"):
			prefix = "试用"
		case strings.Contains(text, "overage"):
			prefix = "超额"
		case strings.Contains(text, "purchased"):
			prefix = "加购"
		case strings.Contains(text, "monthly"):
			prefix = "本月"
		}
		if pair := creditPair.FindStringSubmatch(text); len(pair) > 0 {
			used, _ := num(strings.ReplaceAll(pair[1], ",", ""))
			limit, _ := num(strings.ReplaceAll(pair[2], ",", ""))
			if strings.Contains(text, "used") || strings.Contains(text, "consumed") {
				r.value(prefix+"已用", "积分", used, 0)
				r.value(prefix+"上限", "积分", limit, 0)
			} else if strings.Contains(text, "remaining") || strings.Contains(text, "available") || strings.Contains(text, "left") {
				r.value(prefix+"剩余", "积分", used, 0)
				r.value(prefix+"上限", "积分", limit, 0)
			}
			continue
		}
		values := creditNumber.FindAllString(text, -1)
		if len(values) != 1 {
			continue
		}
		n, ok := num(strings.ReplaceAll(values[0], ",", ""))
		if !ok {
			continue
		}
		var label string
		switch {
		case strings.Contains(text, "remaining"), strings.Contains(text, "available"), strings.Contains(text, "balance"), strings.Contains(text, "left"):
			label = "剩余"
		case strings.Contains(text, "used"), strings.Contains(text, "consumed"):
			label = "已用"
		case strings.Contains(text, "limit"), strings.Contains(text, "total"):
			label = "上限"
		default:
			continue
		}
		r.value(prefix+label, "积分", n, 0)
	}
	if len(r.Meters) == 0 {
		return Reading{}, errors.New("Kiro CLI 未返回可识别的积分明细，请在官方客户端查看 /usage")
	}
	return r, nil
}
