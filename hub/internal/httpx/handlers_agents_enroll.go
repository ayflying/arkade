package httpx

// handlers_agents_enroll.go 实现「预创建服务器 + 专属接入密钥」。
//
// 用户需求原话：「服务器列表添加服务器」，预建记录并下发独立密钥，
// 那台机器上的 Agent 用这把密钥连上来就绑定到这条记录。
//
// 与旧流程的区别：旧流程是 Agent 先连、Hub 自动建记录（共享 token）。
// 新流程是 Hub 先建记录、再有人拿密钥去部署。两条路并存。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

// enrollmentView 是密钥信封。**明文只在这里出现一次。**
//
// 没有任何接口能再次取回它：GET 列表不带、重置会换新的。
// 前端必须当场显示并提示用户保存。
type enrollmentView struct {
	Token   string `json:"token"`
	AgentID string `json:"agent_id"`
}

// createAgentPayload 是新建（预创建）服务器的请求体。
type createAgentPayload struct {
	Name  string            `json:"name"`
	Note  string            `json:"note"`
	Label map[string]string `json:"labels"`
}

// enrollmentResponse 是 create 与 reset 的统一响应。
//
// 两个接口返回同一形状是有意的：前端可以用同一个处理函数
// （显示密钥弹窗 + 更新列表项），不必写两套。
type enrollmentResponse struct {
	Agent      agentView      `json:"agent"`
	Enrollment enrollmentView `json:"enrollment"`
}

// handleCreateAgent 预创建一台服务器记录并下发专属密钥。
//
// 权限：仅 admin。这与"删除服务器"同级——它同样是改变纳管关系的操作。
func handleCreateAgent(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		var req createAgentPayload
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			respondError(w, d.Log, envelope.CodeBadRequest, "请求体格式错误")
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			respondError(w, d.Log, envelope.CodeBadRequest, "服务器名称不能为空")
			return
		}
		if len([]rune(name)) > 128 {
			respondError(w, d.Log, envelope.CodeBadRequest, "服务器名称过长（最多 128 个字符）")
			return
		}
		if len([]rune(req.Note)) > 2000 {
			respondError(w, d.Log, envelope.CodeBadRequest, "备注过长（最多 2000 个字符）")
			return
		}
		// 标签只是展示用的键值对，限制数量与长度避免无意义的膨胀。
		if len(req.Label) > 32 {
			respondError(w, d.Log, envelope.CodeBadRequest, "标签数量过多（最多 32 个）")
			return
		}
		for k, v := range req.Label {
			if strings.TrimSpace(k) == "" || len([]rune(k)) > 64 || len([]rune(v)) > 256 {
				respondError(w, d.Log, envelope.CodeBadRequest, "标签的键或值不合法")
				return
			}
		}

		// 生成密钥。与共享 token 同值会被自动重试避开（否则认证路径有歧义）。
		plain, hash, err := agentlink.GenerateEnrollmentTokenAvoiding(d.AgentSharedToken)
		if err != nil {
			d.Log.Error("生成接入密钥失败", "err", err)
			respondError(w, d.Log, envelope.CodeInternal, "生成接入密钥失败，请重试")
			return
		}

		id, err := store.NewAgentID()
		if err != nil {
			d.Log.Error("生成服务器 ID 失败", "err", err)
			respondError(w, d.Log, envelope.CodeInternal, "创建服务器失败，请重试")
			return
		}

		now := time.Now().UTC()
		rec := store.AgentRecord{
			ID:          id,
			Name:        name,
			DisplayName: name,
			Note:        strings.TrimSpace(req.Note),
			Labels:      req.Label,
			CreatedAt:   now.Unix(),
			UpdatedAt:   now.Unix(),
		}

		if cerr := d.DB.CreatePreprovisionedAgent(r.Context(), rec, hash, now); cerr != nil {
			if errors.Is(cerr, store.ErrAgentNameTaken) {
				respondError(w, d.Log, envelope.CodeConflict, "已存在同名的服务器，请换一个名称")
				return
			}
			respondStoreError(w, d, cerr, "创建服务器失败")
			return
		}

		// 回读一次，保证响应里的 agent 与列表项完全同源（避免手工拼装漏字段）。
		view, ok := d.agentViewByID(r.Context(), id, w)
		if !ok {
			return
		}
		// 含一次性密钥：必须禁止任何形式的缓存。
		respondSecretJSON(w, d.Log, http.StatusCreated, enrollmentResponse{
			Agent:      view,
			Enrollment: enrollmentView{Token: plain, AgentID: id},
		})
	}
}

// handleResetAgentToken 重置某台服务器的专属密钥。
//
// 语义要求（缺一不可）：
//  1. 旧密钥立即失效（库里摘要被替换）；
//  2. 当前连着的那条连接被踢掉，否则它仍能继续收发指令——
//     "重置了密钥但旧连接还能用"会让重置看起来毫无效果；
//  3. 新密钥只在本次响应出现。
func handleResetAgentToken(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}

		// **撤销必须与握手走同一临界区。**
		//
		// 否则存在真实 bypass：握手已通过 authorize、还没登记时，
		// reset 写库并 Close —— 但 Close 在在线表里找不到那条连接，
		// 等于什么都没关；随后那次握手照常上线，旧密钥"重置后仍能用"。
		//
		// 用同一把锁（Server 的 EnrollmentGuard）包住"写库 + 断开"，
		// 与握手的"校验 + 写库 + 登记"完全串行：
		//   撤销先 → 握手在锁内重新校验失败，既不上线也不写库；
		//   握手先 → 这里 Close 能查到并断开它。
		var plain2 string
		if gerr := d.runRevocation(r.Context(), func() error {
			p, h, gerr := agentlink.GenerateEnrollmentTokenAvoiding(d.AgentSharedToken)
			if gerr != nil {
				return gerr
			}
			plain2 = p
			if rerr := d.DB.ResetAgentTokenHash(r.Context(), agentID, h, time.Now().UTC()); rerr != nil {
				return rerr
			}
			d.Registry.Close(agentID, "该服务器的接入密钥已被重置，请使用新密钥重新连接")
			return nil
		}); gerr != nil {
			if errors.Is(gerr, agentlink.ErrGuardBusy) {
				respondError(w, d.Log, envelope.CodeInternal, "系统繁忙，请稍后重试")
				return
			}
			respondStoreError(w, d, gerr, "重置接入密钥失败")
			return
		}

		view, ok := d.agentViewByID(r.Context(), agentID, w)
		if !ok {
			return
		}
		// 含一次性密钥：必须禁止任何形式的缓存。
		respondSecretJSON(w, d.Log, http.StatusOK, enrollmentResponse{
			Agent:      view,
			Enrollment: enrollmentView{Token: plain2, AgentID: agentID},
		})
	}
}

// runRevocation 在握手同一临界区内执行撤销操作。
//
// 没有注入 Guard 时（部分单元测试）直接执行：
// 那种情况下没有在途握手会与本操作竞争，退化为旧行为是安全的。
func (d Deps) runRevocation(ctx context.Context, fn func() error) error {
	if d.Guard == nil {
		return fn()
	}
	return d.Guard.RunRevocation(ctx, fn)
}

// agentViewByID 读取一条记录并转成对外的 agentView。
//
// 单独抽出来是因为 create 与 reset 都要回读，
// 两处各写一遍很容易在将来加字段时漏掉一处，
// 导致"创建时返回的对象"与"列表里的对象"字段不一致——
// 前端用 store.replace 时就会缺字段。
func (d Deps) agentViewByID(ctx context.Context, agentID string, w http.ResponseWriter) (agentView, bool) {
	rec, err := d.DB.GetAgent(ctx, agentID)
	if err != nil {
		respondStoreError(w, d, err, "读取服务器信息失败")
		return agentView{}, false
	}
	return agentMutationView(d, rec), true
}
