// Package pki 实现内部 CA、客户端证书签发与续期所需能力。
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CA 内部证书颁发机构。
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	Key     *ecdsa.PrivateKey
	Pool    *x509.CertPool
}

// LoadOrCreateCA 从目录加载 CA，不存在则生成。
func LoadOrCreateCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	certPEM, errCert := os.ReadFile(certPath)
	keyPEM, errKey := os.ReadFile(keyPath)
	if errCert == nil && errKey == nil {
		cert, key, err := parseCA(certPEM, keyPEM)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(certPEM)
		return &CA{Cert: cert, CertPEM: certPEM, Key: key, Pool: pool}, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "PortTunnel Internal CA", Organization: []string{"PortTunnel"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 失败: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return &CA{Cert: cert, CertPEM: certPEM, Key: key, Pool: pool}, nil
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, fmt.Errorf("CA 证书格式错误")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, fmt.Errorf("CA 私钥格式错误")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// IssueClientCert 根据 CSR 签发客户端证书；CN 使用服务端分配的客户端 ID（不采用 CSR 中的 CN）。
func (ca *CA) IssueClientCert(csrPEM []byte, commonName string, ttl time.Duration) ([]byte, string, time.Time, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", time.Time{}, fmt.Errorf("CSR 格式错误")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("解析 CSR 失败: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", time.Time{}, fmt.Errorf("CSR 签名校验失败: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, "", time.Time{}, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:    now.Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, csr.PublicKey, ca.Key)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("签发客户端证书失败: %w", err)
	}
	signed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, serialHex(signed.SerialNumber), signed.NotAfter, nil
}

// EnsureServerCert 加载或生成服务端证书（自签于内部 CA），SAN 取自 hosts。
// 已有证书未覆盖 hosts（例如新增了访问用的 IP/域名）或临近过期时，会自动重新签发。
func (ca *CA) EnsureServerCert(dir, prefix string, hosts []string, ttl time.Duration) (tls.Certificate, error) {
	certPath := filepath.Join(dir, prefix+".crt")
	keyPath := filepath.Join(dir, prefix+".key")
	if pair, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && len(pair.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(pair.Certificate[0]); err == nil &&
			certProfileOK(leaf) && certCoversHosts(leaf, hosts) && time.Until(leaf.NotAfter) > 30*24*time.Hour {
			return pair, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		// 主体只保留 CN，不写入组织名等产品标识，避免随证书外泄。
		Subject:     pkix.Name{CommonName: firstHost(hosts)},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	seenIP := map[string]bool{}
	seenDNS := map[string]bool{}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			key := ip.String()
			if seenIP[key] {
				continue
			}
			seenIP[key] = true
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			key := strings.ToLower(strings.TrimSuffix(h, "."))
			if seenDNS[key] {
				continue
			}
			seenDNS[key] = true
			tpl.DNSNames = append(tpl.DNSNames, key)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// GenerateCSR 在本地生成私钥并输出 CSR（私钥不出本机）。
func GenerateCSR(keyPath, commonName string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName, Organization: []string{"PortTunnel Client"}},
	}, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// WritePEM 以指定权限写入 PEM 文件。
func WritePEM(path string, data []byte, perm os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, perm)
}

// ParseLeaf 从原始证书中解析叶子证书。
func ParseLeaf(rawCerts [][]byte) (*x509.Certificate, error) {
	if len(rawCerts) == 0 {
		return nil, fmt.Errorf("对端未提供证书")
	}
	return x509.ParseCertificate(rawCerts[0])
}

// VerifyChain 校验对端证书链是否由本 CA 签发。
func (ca *CA) VerifyChain(rawCerts [][]byte) (*x509.Certificate, error) {
	leaf, err := ParseLeaf(rawCerts)
	if err != nil {
		return nil, err
	}
	inter := x509.NewCertPool()
	for _, raw := range rawCerts[1:] {
		if c, err := x509.ParseCertificate(raw); err == nil {
			inter.AddCert(c)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         ca.Pool,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, err
	}
	return leaf, nil
}

func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func serialHex(n *big.Int) string {
	b := n.Bytes()
	if len(b) == 0 {
		return "00"
	}
	return strings.ToUpper(hex.EncodeToString(b))
}

// SerialOf 返回证书序列号的规范字符串（与签发时记录一致）。
func SerialOf(c *x509.Certificate) string {
	if c == nil || c.SerialNumber == nil {
		return ""
	}
	return serialHex(c.SerialNumber)
}

// certCoversHosts 判断证书 SAN 是否覆盖全部 hosts（IP 精确匹配，域名不区分大小写）。
func certCoversHosts(leaf *x509.Certificate, hosts []string) bool {
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			found := false
			for _, have := range leaf.IPAddresses {
				if have.Equal(ip) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
			continue
		}
		found := false
		for _, d := range leaf.DNSNames {
			if strings.EqualFold(strings.TrimSuffix(d, "."), strings.TrimSuffix(h, ".")) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// certProfileOK 判断已签发证书的主体是否符合当前规范：主体中不应包含组织名，
// 以免产品标识随证书外泄（TLS 1.2 下证书为明文）。旧版本签发的证书会因此被重签。
func certProfileOK(leaf *x509.Certificate) bool {
	return len(leaf.Subject.Organization) == 0
}

func firstHost(hosts []string) string {
	for _, h := range hosts {
		if strings.TrimSpace(h) != "" {
			return h
		}
	}
	return "localhost"
}
