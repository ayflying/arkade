package secretbox

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	const plain = "Ha-wlhN6qUKk16XuwNDzSOxL7ywfUx0T"
	sealed, err := box.Seal("arkade.agent.enrollment.v1", plain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, plain) {
		t.Fatalf("密文格式不符合预期: %s", sealed)
	}
	got, err := box.Open("arkade.agent.enrollment.v1", sealed)
	if err != nil || got != plain {
		t.Fatalf("解密结果不一致: %q %v", got, err)
	}
}

func TestOpenRejectsForeignPurposeAndTamperedCipher(t *testing.T) {
	box, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("purpose-a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Open("purpose-b", sealed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("用途标签不同仍解开了: %v", err)
	}

	// 改动密文中间一位必须失败：GCM 的完整性校验要挡得住篡改。
	// 刻意避开头尾：base64 末字符的低位可能落在填充位里，改它不一定改变解码结果。
	tampered := sealed[:len(prefix)+4] + "A" + sealed[len(prefix)+5:]
	if tampered == sealed {
		tampered = sealed[:len(prefix)+4] + "B" + sealed[len(prefix)+5:]
	}
	if _, err := box.Open("purpose-a", tampered); err == nil {
		t.Fatal("篡改后的密文被接受")
	}
}

func TestOpenRejectsMalformedAndWrongKey(t *testing.T) {
	box, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "v1:", "plain-text", "v2:abcd", "v1:!!!", "v1:AA"} {
		if _, err := box.Open("p", bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q 应判为无效: %v", bad, err)
		}
	}

	sealed, err := box.Seal("p", "secret")
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open("p", sealed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("换主密钥后仍解开了: %v", err)
	}
}

func TestNewRequires32ByteKey(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := New(make([]byte, size)); err == nil {
			t.Fatalf("%d 字节主密钥应被拒绝", size)
		}
	}
	if _, err := New(make([]byte, 32)); err != nil {
		t.Fatalf("32 字节主密钥应被接受: %v", err)
	}
}

func TestNilBoxFailsClosed(t *testing.T) {
	var box *Box
	if _, err := box.Seal("p", "x"); err == nil {
		t.Fatal("未初始化的加密器不应加密成功")
	}
	if _, err := box.Open("p", "v1:AAAA"); err == nil {
		t.Fatal("未初始化的加密器不应解密成功")
	}
}
