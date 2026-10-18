package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type iCloudAPIRegisterProvider struct {
	registerProviderBase
	entry    map[string]any
	mailSess *registerSession
}

type iCloudAPIResponse struct {
	Success   bool   `json:"success"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Email     string `json:"email"`
	Subject   string `json:"subject"`
	MessageID string `json:"message_id"`
	Mailbox   struct {
		Email        string `json:"email"`
		APIActive    bool   `json:"api_active"`
		ICloudActive bool   `json:"icloud_active"`
	} `json:"mailbox"`
}

func (p *iCloudAPIRegisterProvider) Name() string { return "icloud_api" }

func (p *iCloudAPIRegisterProvider) Close() {
	if p.mailSess != nil {
		p.mailSess.close()
	}
	p.sess.close()
}

func (p *iCloudAPIRegisterProvider) endpoint(path string) (string, error) {
	raw := strings.TrimSpace(strAny(p.entry["api_base"], ""))
	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("iCloud Privacy Mail API Base 必须是服务根地址，例如 http://127.0.0.1:8787")
	}
	return base.Scheme + "://" + base.Host + path, nil
}

func (p *iCloudAPIRegisterProvider) request(ctx context.Context, method, path string, payload any, out *iCloudAPIResponse) error {
	endpoint, err := p.endpoint(path)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(strAny(p.entry["api_key"], ""))
	if key == "" {
		return errors.New("iCloud Privacy Mail 缺少全局 API Key")
	}
	session := p.sess
	if p.mailSess != nil {
		session = p.mailSess
	}
	return session.requestJSON(ctx, method, endpoint, map[string]string{"Authorization": "Bearer " + key}, payload, out, http.StatusOK)
}

func (p *iCloudAPIRegisterProvider) CreateMailbox(username string) (*registerMailbox, error) {
	if strings.TrimSpace(username) != "" {
		return nil, errors.New("iCloud Privacy Mail 只支持从可用邮箱池自动领取")
	}
	project := strings.TrimSpace(strAny(p.entry["project"], "openai"))
	if project == "" {
		project = "openai"
	}
	var response iCloudAPIResponse
	if err := p.request(context.Background(), http.MethodPost, "/api/v1/mailboxes/claim", map[string]any{
		"project": project,
		"purpose": "register",
		"count":   1,
	}, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		if response.Code == "no_available_mailbox" {
			return nil, registerCapacityExhaustedError{message: "iCloud Privacy Mail 没有可用邮箱"}
		}
		return nil, fmt.Errorf("iCloud Privacy Mail 领取邮箱失败: %s", firstNonEmpty(response.Message, response.Code, "未知错误"))
	}
	address := strings.TrimSpace(response.Mailbox.Email)
	if address == "" || !response.Mailbox.APIActive || !response.Mailbox.ICloudActive {
		return nil, errors.New("iCloud Privacy Mail 返回的邮箱不可用")
	}
	return &registerMailbox{
		Provider:    p.Name(),
		ProviderRef: strAny(p.entry["provider_ref"], ""),
		Label:       strAny(p.entry["label"], ""),
		Address:     address,
		Extra:       map[string]any{"mailbox_provider": p.Name()},
	}, nil
}

func (p *iCloudAPIRegisterProvider) WaitForCode(ctx context.Context, mailbox *registerMailbox) (string, error) {
	if mailbox == nil || strings.TrimSpace(mailbox.Address) == "" {
		return "", errors.New("iCloud Privacy Mail 缺少邮箱地址")
	}
	query := url.Values{"keyword": {"OpenAI"}}
	if !mailbox.CodeNotBefore.IsZero() {
		query.Set("after", mailbox.CodeNotBefore.UTC().Format(time.RFC3339Nano))
	}
	path := "/api/v1/mailboxes/" + url.PathEscape(mailbox.Address) + "/code?" + query.Encode()
	return waitForRegisterCode(ctx, mailbox, intAny(p.entry["wait_timeout"], 30), intAny(p.entry["wait_interval"], 2), func() ([]map[string]any, error) {
		var response iCloudAPIResponse
		if err := p.request(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		if !response.Success {
			if response.Code == "no_code" && response.Retryable {
				return nil, nil
			}
			return nil, fmt.Errorf("iCloud Privacy Mail 取码失败: %s", firstNonEmpty(response.Message, response.Code, "未知错误"))
		}
		if response.Email != "" && !strings.EqualFold(response.Email, mailbox.Address) {
			return nil, errors.New("iCloud Privacy Mail 返回了其他邮箱的验证码")
		}
		if strings.TrimSpace(response.Code) == "" {
			return nil, errors.New("iCloud Privacy Mail 返回空验证码")
		}
		// The API already applies `after`; its returned timestamp loses subsecond precision.
		return []map[string]any{{
			"provider":     p.Name(),
			"mailbox":      mailbox.Address,
			"message_id":   response.MessageID,
			"subject":      response.Subject,
			"text_content": response.Code,
		}}, nil
	})
}
