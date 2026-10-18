package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func waitForIMAPCode(ctx context.Context, email, password string, since time.Time, timeoutSeconds, intervalSeconds int) (string, error) {
	return waitForIMAPCodeWithLogin(ctx, email, email, password, "", since, timeoutSeconds, intervalSeconds)
}

func waitForIMAPCodeWithLogin(ctx context.Context, targetEmail, loginEmail, password, host string, since time.Time, timeoutSeconds, intervalSeconds int) (string, error) {
	result, err := waitForIMAPCodeResultWithLogin(ctx, targetEmail, loginEmail, password, host, since, timeoutSeconds, intervalSeconds)
	if err != nil {
		return "", err
	}
	return result.Code, nil
}

type imapCodeResult struct {
	Code      string
	UID       string
	Recipient string
}

func waitForIMAPCodeResultWithLogin(ctx context.Context, targetEmail, loginEmail, password, host string, since time.Time, timeoutSeconds, intervalSeconds int) (imapCodeResult, error) {
	targetEmail = strings.TrimSpace(targetEmail)
	loginEmail = strings.TrimSpace(loginEmail)
	password = strings.TrimSpace(password)
	if loginEmail == "" || password == "" {
		return imapCodeResult{}, errors.New("IMAP 缺少邮箱或密码")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 3
	}
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	lastErr := ""
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return imapCodeResult{}, err
		}
		result, err := fetchLatestIMAPCodeResultWithLogin(ctx, targetEmail, loginEmail, password, host, since)
		if err == nil && result.Code != "" {
			return result, nil
		}
		if err != nil {
			lastErr = err.Error()
		}
		if !sleepContext(ctx, time.Duration(intervalSeconds)*time.Second) {
			return imapCodeResult{}, ctx.Err()
		}
	}
	if lastErr != "" {
		return imapCodeResult{}, fmt.Errorf("等待邮箱验证码超时，最近错误: %s", lastErr)
	}
	return imapCodeResult{}, errors.New("等待邮箱验证码超时")
}

func fetchLatestIMAPCode(ctx context.Context, email, password string, since time.Time) (string, error) {
	return fetchLatestIMAPCodeWithLogin(ctx, email, email, password, "", since)
}

func fetchLatestIMAPCodeWithLogin(ctx context.Context, targetEmail, loginEmail, password, host string, since time.Time) (string, error) {
	result, err := fetchLatestIMAPCodeResultWithLogin(ctx, targetEmail, loginEmail, password, host, since)
	if err != nil {
		return "", err
	}
	return result.Code, nil
}

func fetchLatestIMAPCodeResultWithLogin(ctx context.Context, targetEmail, loginEmail, password, host string, since time.Time) (imapCodeResult, error) {
	targetEmail = strings.TrimSpace(targetEmail)
	loginEmail = strings.TrimSpace(loginEmail)
	if strings.TrimSpace(host) == "" {
		host = imapHostForEmail(loginEmail)
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", host, &tls.Config{ServerName: strings.Split(host, ":")[0]})
	if err != nil {
		return imapCodeResult{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	reader := bufio.NewReader(conn)
	if _, err := readIMAPLine(reader); err != nil {
		return imapCodeResult{}, err
	}
	if err := imapCommand(conn, reader, "a001", "LOGIN "+imapQuote(loginEmail)+" "+imapQuote(password)); err != nil {
		return imapCodeResult{}, err
	}
	if err := imapCommand(conn, reader, "a002", "SELECT INBOX"); err != nil {
		return imapCodeResult{}, err
	}
	searchResp, err := imapCommandCollect(conn, reader, "a003", "UID SEARCH ALL")
	if err != nil {
		return imapCodeResult{}, err
	}
	ids := imapSearchIDs(searchResp)
	for left, right := 0, len(ids)-1; left < right; left, right = left+1, right-1 {
		ids[left], ids[right] = ids[right], ids[left]
	}
	if len(ids) > 50 {
		ids = ids[:50]
	}
	seenRecipients := []string{}
	for _, id := range ids {
		body, err := imapFetchBody(conn, reader, id)
		if err != nil {
			continue
		}
		recipients := imapMessageRecipients(body)
		if strings.TrimSpace(recipients) != "" {
			seenRecipients = appendUniqueIMAPRecipient(seenRecipients, recipients)
		}
		receivedAt := time.Now().UTC().Format(time.RFC3339Nano)
		if ts := imapMessageDate(body); !ts.IsZero() {
			receivedAt = ts.UTC().Format(time.RFC3339Nano)
		}
		msg := normalizeGenericMailMessage(map[string]any{
			"id":          id,
			"provider":    "imap",
			"to":          recipients,
			"received_at": receivedAt,
			"body":        body,
			"text":        body,
			"raw":         body,
		})
		if !since.IsZero() && messageBeforeCodeBoundary(&registerMailbox{CodeNotBefore: since}, msg) {
			continue
		}
		matchedTarget := targetEmail == "" || imapMessageMatchesTarget(msg, targetEmail)
		if !matchedTarget {
			continue
		}
		if code := extractRegisterCode(msg); code != "" {
			return imapCodeResult{Code: code, UID: id, Recipient: imapMatchedRecipient(body, targetEmail)}, nil
		}
		if targetEmail != "" {
			return imapCodeResult{}, fmt.Errorf("已扫到目标邮箱邮件但未提取到验证码，目标=%s，收件人=%s", targetEmail, firstNonEmpty(imapMatchedRecipient(body, targetEmail), recipients))
		}
	}
	if targetEmail != "" && len(seenRecipients) > 0 {
		return imapCodeResult{}, fmt.Errorf("未找到目标邮箱验证码，目标=%s，最近邮件收件人=%s", targetEmail, strings.Join(seenRecipients, " | "))
	}
	return imapCodeResult{}, nil
}

func deleteIMAPMessageWithLogin(ctx context.Context, loginEmail, password, host, uid string) error {
	loginEmail = strings.TrimSpace(loginEmail)
	password = strings.TrimSpace(password)
	uid = strings.TrimSpace(uid)
	if loginEmail == "" || password == "" || uid == "" {
		return nil
	}
	if strings.TrimSpace(host) == "" {
		host = imapHostForEmail(loginEmail)
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", host, &tls.Config{ServerName: strings.Split(host, ":")[0]})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	reader := bufio.NewReader(conn)
	if _, err := readIMAPLine(reader); err != nil {
		return err
	}
	if err := imapCommand(conn, reader, "d001", "LOGIN "+imapQuote(loginEmail)+" "+imapQuote(password)); err != nil {
		return err
	}
	if err := imapCommand(conn, reader, "d002", "SELECT INBOX"); err != nil {
		return err
	}
	if err := imapCommand(conn, reader, "d003", "UID STORE "+uid+" +FLAGS.SILENT (\\Deleted)"); err != nil {
		return err
	}
	return imapCommand(conn, reader, "d004", "EXPUNGE")
}

func imapMessageDate(body string) time.Time {
	re := regexp.MustCompile(`(?im)^Date:\s*(.+?)$`)
	matches := re.FindStringSubmatch(body)
	if len(matches) < 2 {
		return time.Time{}
	}
	ts, err := mail.ParseDate(strings.TrimSpace(matches[1]))
	if err != nil {
		return time.Time{}
	}
	return ts
}

func imapMessageRecipients(body string) string {
	return strings.Join(imapHeaderValues(body, imapRecipientHeaderNames()...), "\n")
}

func imapMatchedRecipient(body, targetEmail string) string {
	target := strings.ToLower(strings.TrimSpace(targetEmail))
	if target == "" {
		return ""
	}
	for _, value := range imapHeaderValues(body, imapRecipientHeaderNames()...) {
		if strings.Contains(strings.ToLower(value), target) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func imapMessageMatchesTarget(message map[string]any, targetEmail string) bool {
	if messageMatchesEmail(message, targetEmail) {
		return true
	}
	target := strings.ToLower(strings.TrimSpace(targetEmail))
	if target == "" {
		return true
	}
	searchable := strings.ToLower(imapNormalizeQuotedPrintableText(strings.Join([]string{
		strAny(message["to"], ""),
		strAny(message["text_content"], ""),
		strAny(message["html_content"], ""),
		strAny(message["raw"], ""),
	}, "\n")))
	if strings.Contains(searchable, target) {
		return true
	}
	local, domain, ok := strings.Cut(target, "@")
	if !ok || local == "" {
		return false
	}
	if domain == "icloud.com" || domain == "me.com" || domain == "mac.com" || domain == "gmail.com" || domain == "googlemail.com" {
		return len(local) >= 6 && strings.Contains(searchable, local)
	}
	return false
}

func imapNormalizeQuotedPrintableText(value string) string {
	value = strings.ReplaceAll(value, "=\r\n", "")
	value = strings.ReplaceAll(value, "=\n", "")
	replacer := strings.NewReplacer(
		"=40", "@", "=2E", ".", "=2e", ".",
		"=5F", "_", "=5f", "_",
		"=2D", "-", "=2d", "-",
		"=2B", "+", "=2b", "+",
		"=3D", "=",
	)
	return replacer.Replace(value)
}

func imapRecipientHeaderNames() []string {
	return []string{
		"To",
		"Cc",
		"Delivered-To",
		"X-Delivered-To",
		"X-Original-To",
		"X-Original-Recipient",
		"Original-Recipient",
		"X-Forwarded-To",
		"X-Envelope-To",
		"Envelope-To",
		"Apparently-To",
		"Resent-To",
		"X-Apple-Original-To",
		"X-Apple-Hme-Recipient",
		"X-Apple-Private-Relay",
	}
}

func appendUniqueIMAPRecipient(items []string, value string) []string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return items
	}
	if len(value) > 220 {
		value = value[:220] + "..."
	}
	lower := strings.ToLower(value)
	for _, item := range items {
		if strings.ToLower(item) == lower {
			return items
		}
	}
	if len(items) >= 5 {
		return items
	}
	return append(items, value)
}

func imapHeaderValues(body string, names ...string) []string {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[strings.ToLower(strings.TrimSpace(name))] = true
	}
	values := []string{}
	currentName := ""
	var currentValue strings.Builder
	flush := func() {
		if currentName == "" {
			return
		}
		if wanted[strings.ToLower(currentName)] {
			values = append(values, strings.TrimSpace(currentValue.String()))
		}
		currentName = ""
		currentValue.Reset()
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			break
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if currentName != "" {
				currentValue.WriteByte(' ')
				currentValue.WriteString(strings.TrimSpace(line))
			}
			continue
		}
		flush()
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		currentName = strings.TrimSpace(name)
		currentValue.WriteString(strings.TrimSpace(value))
	}
	flush()
	return values
}

func imapHostForEmail(email string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@")
	if len(parts) != 2 {
		return "imap.gmail.com:993"
	}
	domain := parts[1]
	switch {
	case domain == "outlook.com" || domain == "hotmail.com" || domain == "live.com" || domain == "msn.com":
		return "outlook.office365.com:993"
	case domain == "icloud.com" || domain == "me.com" || domain == "mac.com":
		return "imap.mail.me.com:993"
	case domain == "gmail.com" || domain == "googlemail.com":
		return "imap.gmail.com:993"
	case domain == "yahoo.com" || domain == "ymail.com":
		return "imap.mail.yahoo.com:993"
	default:
		return "imap." + domain + ":993"
	}
}

func imapQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func readIMAPLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func imapCommand(conn net.Conn, reader *bufio.Reader, tag, command string) error {
	_, err := imapCommandCollect(conn, reader, tag, command)
	return err
}

func imapCommandCollect(conn net.Conn, reader *bufio.Reader, tag, command string) ([]string, error) {
	if _, err := fmt.Fprintf(conn, "%s %s\r\n", tag, command); err != nil {
		return nil, err
	}
	lines := []string{}
	for {
		line, err := readIMAPLine(reader)
		if err != nil {
			return lines, err
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, tag+" ") {
			if strings.Contains(strings.ToUpper(line), " OK") {
				return lines, nil
			}
			return lines, errors.New(line)
		}
	}
}

func imapSearchIDs(lines []string) []string {
	out := []string{}
	for _, line := range lines {
		if !strings.HasPrefix(strings.ToUpper(line), "* SEARCH") {
			continue
		}
		for _, part := range strings.Fields(line) {
			if _, err := strconv.Atoi(part); err == nil {
				out = append(out, part)
			}
		}
	}
	return out
}

func imapFetchBody(conn net.Conn, reader *bufio.Reader, id string) (string, error) {
	tag := "f" + randID(6)
	if _, err := fmt.Fprintf(conn, "%s UID FETCH %s BODY.PEEK[]\r\n", tag, id); err != nil {
		return "", err
	}
	literalRe := regexp.MustCompile(`\{(\d+)\}$`)
	var body strings.Builder
	for {
		line, err := readIMAPLine(reader)
		if err != nil {
			return body.String(), err
		}
		if matches := literalRe.FindStringSubmatch(line); len(matches) == 2 {
			n, _ := strconv.Atoi(matches[1])
			if n > 2*1024*1024 {
				return "", fmt.Errorf("IMAP 邮件过大: %d bytes", n)
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(reader, buf); err != nil {
				return body.String(), err
			}
			body.Write(buf)
			continue
		}
		if strings.HasPrefix(line, tag+" ") {
			if strings.Contains(strings.ToUpper(line), " OK") {
				return body.String(), nil
			}
			return body.String(), errors.New(line)
		}
	}
}
