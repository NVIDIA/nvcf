package credentials

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/volcengine/volcengine-go-sdk/volcengine/response"
)

var _ Provider = &OIDCCredentialsProvider{}

const DefaultEndpoint = "sts.volcengineapi.com"

const (
	defaultSessionName = "volcengine-go-sdk-oidc-session"
)

type HttpOptions struct {
	// The maximum time to wait for a connection.
	Timeout time.Duration
}

// OIDCCredentials 包含访问凭证信息
type OIDCCredentials struct {
	CurrentTime     string `json:"CurrentTime,omitempty"`
	Expiration      string `json:"Expiration,omitempty"`
	AccessKeyId     string `json:"AccessKeyId,omitempty"`
	SecretAccessKey string `json:"SecretAccessKey,omitempty"`
	SessionToken    string `json:"SessionToken,omitempty"`
}

// OIDCTokenInfo 包含 OIDC 令牌的信息
type OIDCTokenInfo struct {
	Subject        string   `json:"Subject,omitempty"`
	Issuer         string   `json:"Issuer,omitempty"`
	ClientIds      []string `json:"ClientIds,omitempty"`
	ExpirationTime string   `json:"ExpirationTime,omitempty"`
	IssuanceTime   string   `json:"IssuanceTime,omitempty"`
}

// AssumedRoleUser 包含假定角色用户的信息
type AssumedRoleUser struct {
	AssumedRoleId string `json:"AssumedRoleId,omitempty"`
	Trn           string `json:"Trn,omitempty"`
}

// Result 包含请求结果的详细信息
type Result struct {
	Credentials     OIDCCredentials `json:"Credentials,omitempty"`
	OIDCTokenInfo   OIDCTokenInfo   `json:"OIDCTokenInfo,omitempty"`
	AssumedRoleUser AssumedRoleUser `json:"AssumedRoleUser,omitempty"`
}

// AssumeRoleWithOIDCResponse 表示使用 OIDC 进行角色假定的响应结构体
type AssumeRoleWithOIDCResponse struct {
	ResponseMetadata response.ResponseMetadata `json:"ResponseMetadata,omitempty"`
	Result           Result                    `json:"Result,omitempty"`
}

type OIDCCredentialsProvider struct {
	OIDCTokenFilePath string
	RoleTrn           string
	RoleSessionName   string
	DurationSeconds   int
	Policy            string
	// for sts endpoint
	Schema              string
	Endpoint            string
	lastUpdateTimestamp int64
	expirationTimestamp int64
	sessionValue        *Value
	// for http options
	httpOptions *HttpOptions
}

func NewOIDCCredentialsProviderFromEnv() *OIDCCredentialsProvider {
	return &OIDCCredentialsProvider{
		OIDCTokenFilePath: os.Getenv("VOLCENGINE_OIDC_TOKEN_FILE"),
		RoleTrn:           os.Getenv("VOLCENGINE_OIDC_ROLE_TRN"),
		RoleSessionName:   os.Getenv("VOLCENGINE_OIDC_ROLE_SESSION_NAME"),
		DurationSeconds:   3600, // default duration
		Policy:            os.Getenv("VOLCENGINE_OIDC_ROLE_POLICY"),
		Schema:            "https", //default https
		Endpoint:          os.Getenv("VOLCENGINE_OIDC_STS_ENDPOINT"),
		httpOptions:       &HttpOptions{Timeout: 10 * time.Second},
	}
}
func (p *OIDCCredentialsProvider) Retrieve() (Value, error) {
	return p.fetchOnce()
}

func (p *OIDCCredentialsProvider) fetchOnce() (Value, error) {
	// 检查必要参数
	if p.OIDCTokenFilePath == "" || p.RoleTrn == "" {
		return Value{}, fmt.Errorf("missing required environment variables: VOLCENGINE_OIDC_TOKEN_FILE or VOLCENGINE_OIDC_ROLE_TRN")
	}

	// 从文件中读取OIDC令牌
	tokenBytes, err := ioutil.ReadFile(p.OIDCTokenFilePath)
	if err != nil {
		return Value{}, fmt.Errorf("failed to read OIDC token file: %v", err)
	}
	oidcToken := string(tokenBytes)

	// 构建请求参数
	data := url.Values{}
	data.Set("RoleTrn", p.RoleTrn)
	data.Set("OIDCToken", oidcToken)
	if p.RoleSessionName != "" {
		data.Set("RoleSessionName", p.RoleSessionName)
	} else {
		data.Set("RoleSessionName", defaultSessionName)
	}
	data.Set("DurationSeconds", fmt.Sprintf("%d", p.DurationSeconds+60))
	if p.Policy != "" {
		data.Set("Policy", p.Policy)
	}
	endpoint := DefaultEndpoint
	if p.Endpoint != "" {
		endpoint = p.Endpoint
	}
	scheme := "https"
	if p.Schema != "" {
		scheme = p.Schema
	}

	url := scheme + "://" + endpoint
	var client *http.Client
	if p.httpOptions != nil {
		client = &http.Client{Timeout: p.httpOptions.Timeout}
	} else {
		client = &http.Client{}
	}
	// 发送请求到STS服务
	resp, err := client.Post(
		url+"/?Action=AssumeRoleWithOIDC&Version=2018-01-01",
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return Value{}, fmt.Errorf("failed to request STS service: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return Value{}, fmt.Errorf("STS service returned non-OK status: %d, body: %s", resp.StatusCode, string(body))
	}
	stsResp := AssumeRoleWithOIDCResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&stsResp); err != nil {
		return Value{}, fmt.Errorf("failed to decode STS response: %v", err)
	}
	if stsResp.ResponseMetadata.Error != nil {
		return Value{}, fmt.Errorf("failed to decode STS response: %v", stsResp.ResponseMetadata)
	}

	// 创建凭证对象
	creds := Value{
		AccessKeyID:     stsResp.Result.Credentials.AccessKeyId,
		SecretAccessKey: stsResp.Result.Credentials.SecretAccessKey,
		SessionToken:    stsResp.Result.Credentials.SessionToken,
		ProviderName:    "OIDC",
	}
	p.lastUpdateTimestamp = time.Now().Unix()
	// parse time 2021-04-12T11:57:09+08:00 to unix timestamp
	expirationTime, err := time.Parse(time.RFC3339, stsResp.Result.Credentials.Expiration)
	if err != nil {
		return Value{}, fmt.Errorf("failed to parse expiration time: %v", err)
	}
	p.expirationTimestamp = expirationTime.Unix()
	p.sessionValue = &creds
	return creds, nil
}

func (p *OIDCCredentialsProvider) IsExpired() bool {
	if p.expirationTimestamp == 0 {
		return true
	}
	return time.Now().Unix() > p.expirationTimestamp
}
