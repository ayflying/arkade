package user

// crypto.go 集中放密码哈希的底层调用，便于未来更换算法时只改这一处。
//
// 放在独立文件的原因：password.go 表达的是「密码策略」（长度、上限、提示文案），
// 本文件表达的是「用哪个算法」。两者变化原因不同，混在一起会在换算法时连带改动策略逻辑。

import "golang.org/x/crypto/bcrypt"

// bcryptGenerate 生成 bcrypt 哈希。
func bcryptGenerate(plain []byte, cost int) ([]byte, error) {
	return bcrypt.GenerateFromPassword(plain, cost)
}

// bcryptCompare 比对明文与哈希。
func bcryptCompare(hashed, plain []byte) error {
	return bcrypt.CompareHashAndPassword(hashed, plain)
}
