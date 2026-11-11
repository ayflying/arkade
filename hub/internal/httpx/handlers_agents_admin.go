package httpx

// handlers_agents_admin.go 是服务器（Agent）记录的管理接口：改名/备注/禁用/删除。
//
// 全部要求 admin 角色。理由：这些操作改的是"纳管关系"本身
// （谁能连、这台机器在系统里叫什么、还要不要它），
// 影响面覆盖所有用户组与所有编排，不属于任何单台服务器的 operate/manage 档位能覆盖的。
// 特别注意"禁用"与"删除"实际上能切断别人的运维通道，必须由管理员执行。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"


	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

// agentAdminPatchRequest 是 PATCH /api/agents/{agentID} 的请求体。
//
// 全部用指针：nil = 本次不改。
// 这样"把备注清空"（传空串）与"不动备注"（不传字段）才能区分开。
type agentAdminPatchRequest struct {
	DirectAddress *string            `json:"direct_address"`
	DisplayName   *string            `json:"display_name"`
	Note          *string            `json:"note"`
	Labels        *map[string]string `json:"labels"`
}

// 字段长度上限。
//
// 设上限的原因不是存储限制，而是界面：显示名与备注都会渲染在列表行里，
// 超长文本会把布局撑坏，且这类字段没有任何需要很长的合理场景。
const (
	maxAgentDisplayNameLen = 64
	maxAgentNoteLen        = 500
	maxAgentLabelCount     = 32
	maxAgentLabelKeyLen    = 64
	maxAgentLabelValueLen  = 256
)

// handlePatchAgent 处理 PATCH /api/agents/{agentID}。
func handlePatchAgent(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		agentID := pathParam(r, "agentID")

		var req agentAdminPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, d.Log, envelope.CodeBadRequest, "请求格式错误: "+err.Error())
			return
		}

		fields := store.AgentAdminFields{}
		if req.DirectAddress != nil {
			v, err := normalizeDirectAddress(*req.DirectAddress)
			if err != nil {
				badRequest(w, d, "%s", err.Error())
				return
			}
			fields.DirectAddress = &v
		}
		if req.DisplayName != nil {
			v := strings.TrimSpace(*req.DisplayName)
			if len([]rune(v)) > maxAgentDisplayNameLen {
				respondError(w, d.Log, envelope.CodeBadRequest, "显示名不能超过 64 个字符")
				return
			}
			fields.DisplayName = &v
		}
		if req.Note != nil {
			v := strings.TrimSpace(*req.Note)
			if len([]rune(v)) > maxAgentNoteLen {
				respondError(w, d.Log, envelope.CodeBadRequest, "备注不能超过 500 个字符")
				return
			}
			fields.Note = &v
		}
		if req.Labels != nil {
			if msg := validateAgentLabels(*req.Labels); msg != "" {
				respondError(w, d.Log, envelope.CodeBadRequest, msg)
				return
			}
			v := *req.Labels
			fields.Labels = &v
		}

		rec, err := d.DB.UpdateAgentAdmin(r.Context(), agentID, fields, time.Now().Unix())
		if err != nil {
			respondStoreError(w, d, err, "服务器不存在")
			return
		}
		respondJSON(w, d.Log, http.StatusOK, agentMutationPayload{Agent: agentMutationView(d, rec)})
	}
}

// validateAgentLabels 校验标签，返回空串表示通过。
func validateAgentLabels(labels map[string]string) string {
	if len(labels) > maxAgentLabelCount {
		return "标签数量不能超过 32 个"
	}
	for k, v := range labels {
		if strings.TrimSpace(k) == "" {
			return "标签名不能为空"
		}
		if len([]rune(k)) > maxAgentLabelKeyLen {
			return "标签名不能超过 64 个字符"
		}
		if len([]rune(v)) > maxAgentLabelValueLen {
			return "标签值不能超过 256 个字符"
		}
	}
	return ""
}

// DeleteAgentCloseReason 是删除服务器时回给 Agent 的断开原因。
//
// 必须写成人能照做的中文：它会出现在 Agent 的日志和界面上，
// 运维据此判断"是被管理员移除了"而不是网络故障——
// 后者会导致无意义的重连风暴与错误排查方向。
const DeleteAgentCloseReason = "该服务器记录已被管理员移除，连接已断开；如需继续纳管请重新接入"

// handleDeleteAgent 处理 DELETE /api/agents/{agentID}。
//
// 语义要点（本功能最容易做错的地方）：
//
//  1. **不删除真实的容器**。删除的只是"这台机器归系统管"的登记，
//     机器上跑的东西原样不动。界面上必须二次确认并说清这点，
//     否则用户会以为删掉了服务器。
//  2. **不删除 compose_stacks**。编排内容是用户手写的资产，
//     不能因为服务器记录没了就跟着消失。删完这些编排变成"孤儿"，
//     由界面的孤儿机制提示处理——这比静默删除安全得多。
//  3. **不删除 group_agents 授权**。同理，属于用户的权限配置。
//
// 返回被"遗落"的编排数量，让界面能立刻提示"有 N 条编排现在没有归属服务器"。
func handleDeleteAgent(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		agentID := pathParam(r, "agentID")

		// 先统计该服务器名下有多少编排，删除后它们会成为孤儿。
		//
		// 必须在删除前统计：删完之后虽然还能全局统计孤儿总数，
		// 但那个数字包含历史遗留的孤儿，无法回答"这次操作产生了多少"，
		// 而用户真正关心的是后者。
		before, err := d.DB.CountComposeStacks(r.Context(), agentID)
		if err != nil {
			respondStoreError(w, d, err, "统计编排数量失败")
			return
		}

		// **删除必须与握手走同一临界区。**
		//
		// 否则存在真实的"复活"竞态：一次握手已通过 authorize、
		// 还没写库时，删除操作把记录删掉；随后那次握手照常
		// OnAgentHello 落库，把刚删掉的服务器**重新建出来**。
		// 用户看到的是"删掉的服务器自己回来了"。
		var deleteErr error
		if gerr := d.runRevocation(r.Context(), func() error {
			if err := d.DB.DeleteAgent(r.Context(), agentID); err != nil {
				return err
			}
			// 同时删除专属凭据：不删的话那把密钥仍然有效，
			// 持有它的机器下次重连会按摘要解析出一个不存在的 ID。
			if terr := d.DB.DeleteAgentToken(r.Context(), agentID); terr != nil {
				// 记录已删除，凭据删失败属于罕见故障。
				// 只记日志而不判请求失败：记录确实没了，
				// 回 500 会让用户以为删除没成功而反复点击。
				d.Log.Error("删除服务器后清理接入凭据失败，该凭据可能仍然可用",
					"agent_id", agentID, "err", terr)
			}
			// 断开现有连接（详见下方说明）。
			d.Registry.Close(agentID, DeleteAgentCloseReason)
			return nil
		}); gerr != nil {
			deleteErr = gerr
		}
		if deleteErr != nil {
			if errors.Is(deleteErr, agentlink.ErrGuardBusy) {
				respondError(w, d.Log, envelope.CodeInternal, "系统繁忙，请稍后重试")
				return
			}
			respondStoreError(w, d, deleteErr, "服务器不存在")
			return
		}

		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"deleted":  true,
			"agent_id": agentID,
			// 该服务器名下有多少份配置会失去归属服务器。
			"orphan_stacks": before,
			// 提示语由后端给而不是让前端自己拼：这段话必须准确表达
			// "删的是登记，不是机器"，统一措辞可以避免各处叫法不一致
			// 造成用户误操作。
			//
			// 措辞特别注意：这里**不能**说编排会变成 Agent 侧的那个 orphans。
			// 那是两个方向相反的概念：
			//   - 服务器侧 orphans（本处的 orphan_stacks）：Hub 的配置记录
			//     失去了归属服务器，配置内容仍在 Hub 里；
			//   - Agent 侧 orphans：机器上有容器在跑，但 Hub 没有对应记录。
			// 把两者混为一谈会让用户以为"配置跑到机器上去了"。
			"message": "服务器记录已删除，机器上的容器未受影响；原有的编排配置仍保留在服务端，重新接入该服务器后可继续使用",
		})
	}
}

// handleDisableAgent 处理 POST /api/agents/{agentID}/disable。
//
// 禁用后该 Agent 重连会在握手阶段被拒绝并收到明确原因
// （不是直接断开——那样 Agent 只能看到"连不上"，运维无从排查）。
// 若它当前在线，同时主动踢下线，避免"已禁用却还连着"的矛盾状态。
func handleDisableAgent(d Deps) http.HandlerFunc {
	return handleSetAgentDisabled(d, true)
}

// handleEnableAgent 处理 POST /api/agents/{agentID}/enable。
func handleEnableAgent(d Deps) http.HandlerFunc {
	return handleSetAgentDisabled(d, false)
}

// handleSetAgentDisabled 是禁用/启用的共用实现。
func handleSetAgentDisabled(d Deps, disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		agentID := pathParam(r, "agentID")

		// 写库与踢连接必须在临界区内。
		//
		// 竞争的不是"禁用后被踢"，而是**在途握手**：
		// 一次握手已过 authorize、还没落库时被禁用，
		// 若不禁用路径拿同一把锁，那次握手会在禁用生效后照常上线，
		// 于是"已禁用"的服务器又挂在线上——禁用看起来没生效。
		var setErr error
		if gerr := d.runRevocation(r.Context(), func() error {
			if err := d.DB.SetAgentDisabled(r.Context(), agentID, disabled, time.Now().Unix()); err != nil {
				return err
			}
			// 禁用时把在线连接踢掉。不踢的话就有一个危险的空档：
			// 管理员禁用了一个疑似异常的 Agent，但它已经连着，
			// 而握手只发生在新连接上，禁用对存量连接无效——
			// 用户看到"已禁用"而那条通道还能用。
			if disabled {
				d.Registry.Close(agentID, "该服务器已被管理员禁用，连接已断开")
			}
			return nil
		}); gerr != nil {
			setErr = gerr
		}
		if setErr != nil {
			if errors.Is(setErr, agentlink.ErrGuardBusy) {
				respondError(w, d.Log, envelope.CodeInternal, "系统繁忙，请稍后重试")
				return
			}
			respondStoreError(w, d, setErr, "服务器不存在")
			return
		}

		rec, err := d.DB.GetAgent(r.Context(), agentID)
		if err != nil {
			respondStoreError(w, d, err, "服务器不存在")
			return
		}
		respondJSON(w, d.Log, http.StatusOK, agentMutationPayload{Agent: agentMutationView(d, rec)})
	}
}

// agentMutationPayload 是改名/禁用/启用接口的响应体会。
//
// **复用列表的 agentView，而不是手写一个 map。**
//
// 这不是整洁度的考虑，而是契约一致性的硬要求。曾经这里的响应是手写的
// map，用了 agent_id 与 runtime_ver，而列表接口用 id 与 runtime_version。
// 前端拿到 PATCH 响应后调用 store.replace(updated) 找 id —— 找不到，
// 于是静默不更新。没有报错、没有异常，只有"改完名字界面上没变"，
// 用户以为操作失败，反复点。
//
// 复用同一个结构体后，两个接口的字段名在**编译期**就绑定了：
// 改一处必然同时改另一处，不可能再分叉。
type agentMutationPayload struct {
	Agent agentView `json:"agent"`
}

// agentMutationView 把记录转成与列表项形状完全一致的视图。
//
// 在线状态用 Registry 推导（Registry.Status 会把数据库里过期的
// "online" 降级成 "offline"），与列表接口保持同一口径。
func agentMutationView(d Deps, rec store.AgentRecord) agentView {
	online := map[string]struct{}{}
	if d.Registry.IsOnline(rec.ID) {
		online[rec.ID] = struct{}{}
	}
	return toAgentView(rec, online)
}

// respondStoreError 已存在于 handlers_admin.go，全项目共用一份映射口径
// （ErrNotFound→404、ErrConflict→409、其余→500），此处直接复用而不重写。
