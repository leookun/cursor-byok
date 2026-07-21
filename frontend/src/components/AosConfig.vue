<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import { showModal } from "@/composables/useModal";
import {
  appState,
  QUALITY_TIER_OPTIONS,
  reloadUserConfig,
  saveAOSConfig,
  recognizeAOSMembers,
} from "@/state/appState";
import { computed, onMounted, ref } from "vue";

const qualityTierOptions = QUALITY_TIER_OPTIONS;

const adapterOptions = computed(() =>
  appState.modelAdapters
    .filter((a) => (a.ref || a.id) && a.displayName)
    .map((a) => ({
      // label 显示「供应商:模型 / 显示名」，value 用全局唯一 Ref
      label: a.ref
        ? `${a.ref}  ·  ${a.displayName}`
        : a.displayName,
      value: a.ref || a.id,
    })),
);

// Recognition state — tracks whether the Leader is currently "meeting" the team
// and the tags it inferred per member. Tags are not user-editable; they are
// populated by clicking "认识组员" which calls AOSModel.RecognizeMembers.
const recognizing = ref(false);

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || "服务错误").trim() || "服务错误",
  });
}

async function handleSaveAll() {
  const aosConfig = {
    ...appState.aosConfig,
    // Tags are no longer persisted in user config — they are runtime-only,
    // set by AOSModel.RecognizeMembers. Strip them here so saveAOSConfig
    // doesn't try to send them (the backend AOSMemberConfig has no Tags field).
    members: appState.aosConfig.members.map((m) => ({
      id: m.id,
      name: m.name,
      adapterID: m.adapterID,
      systemPrompt: m.systemPrompt,
    })),
  };
  const result = await saveAOSConfig(aosConfig);
  if (!result.ok) {
    await showActionError("保存失败", result.error);
    return;
  }
  await showModal({ title: "提示", content: "配置已保存" });
}

function handleAddAOSMember() {
  appState.aosConfig.members.push({
    id: `member-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    name: "",
    adapterID: "",
    systemPrompt: "",
  });
  // New members have no tags yet; clear any stale recognition state.
  appState.aosRecognition = { members: [], error: "" };
}

function handleRemoveAOSMember(index) {
  appState.aosConfig.members.splice(index, 1);
  // Removing a member invalidates the recognition cache.
  appState.aosRecognition = { members: [], error: "" };
}

// tagsForMember returns the tags the Leader inferred for a given member ID,
// or null when no recognition has run yet (or the member was added after).
function tagsForMember(memberId) {
  if (!appState.aosRecognition?.members) {
    return null;
  }
  const found = appState.aosRecognition.members.find((m) => m.id === memberId);
  return found && Array.isArray(found.tags) ? found.tags : null;
}

async function handleRecognizeMembers() {
  if (recognizing.value) {
    return;
  }
  // Require at least one member with an adapter + name + prompt so the Leader
  // has something meaningful to read.
  const ready = appState.aosConfig.members.filter(
    (m) => m.adapterID && m.name && m.systemPrompt,
  );
  if (ready.length === 0) {
    await showActionError(
      "无法认识组员",
      "请先添加至少一位成员，并填写名称、适配器和 System Prompt。",
    );
    return;
  }
  // Leader adapter must be configured.
  if (!appState.aosConfig.leader?.adapterID) {
    await showActionError(
      "无法认识组员",
      "请先为 Leader 绑定模型适配器（Leader 需要亲自读每位成员的描述）。",
    );
    return;
  }
  recognizing.value = true;
  try {
    // 先落盘当前 AOS 配置（含未点「保存」的编辑），后端从磁盘/Host 配置读成员列表。
    const saveResult = await saveAOSConfig({
      ...appState.aosConfig,
      members: appState.aosConfig.members.map((m) => ({
        id: m.id,
        name: m.name,
        adapterID: m.adapterID,
        systemPrompt: m.systemPrompt,
      })),
    });
    if (!saveResult.ok) {
      await showActionError("保存配置失败", saveResult.error);
      return;
    }

    const result = await recognizeAOSMembers();
    appState.aosRecognition = result;
    if (!result.ok) {
      await showActionError("认识组员失败", result.error);
      return;
    }
    if (result.error) {
      // Partial success — Leader returned something but parsing warned.
      await showActionError("认识组员部分成功", result.error);
      return;
    }
    await showModal({
      title: "认识完成",
      content: `Leader 已为 ${result.members.length} 位成员生成路由标签。`,
    });
  } catch (err) {
    await showActionError("认识组员失败", err);
  } finally {
    recognizing.value = false;
  }
}

onMounted(async () => {
  await reloadUserConfig().catch((err) => {
    console.warn("[AOS Config] 加载配置失败，使用默认值", err);
  });
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <!-- 页面标题 -->
    <div class="flex items-center justify-between border-b border-[#2a2a2a] pb-3">
      <h1 class="text-lg font-semibold text-white">AOS 配置</h1>
      <Button variant="primary" :disabled="appState.configSaving" @click="handleSaveAll">
        {{ appState.configSaving ? "保存中..." : "保存配置" }}
      </Button>
    </div>

    <!-- Optimization Runtime -->
    <Card>
      <div class="flex flex-col gap-4">
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">Optimization Runtime</h2>
            <div class="text-xs text-[#a3a3a3]">
              Token 预算分配与成本优化：Quality Tier 影响 MOA 专家选模策略；月度预算用于自动降级
            </div>
          </div>
          <label class="flex cursor-pointer items-center gap-2 text-sm text-[#d4d4d4]">
            <input
              v-model="appState.optimizationEnabled"
              type="checkbox"
              class="h-4 w-4 rounded border-[#404040] bg-[#171717] text-[#3b82f6]"
            />
            启用
          </label>
        </div>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-[#a3a3a3]">Quality Tier</div>
            <Select
              v-model="appState.optimizationQualityTier"
              :options="qualityTierOptions"
              placeholder="选择质量等级"
              :disabled="!appState.optimizationEnabled"
            />
          </div>
        </div>
      </div>
    </Card>

    <!-- AOS 核心配置（含 Members） -->
    <Card>
      <div class="flex flex-col gap-4">
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">AOS (AI Organization System)</h2>
            <div class="text-sm text-[#a3a3a3]">
              组织级 Virtual Model：Leader / Members / Workspace / Sprint 编排
            </div>
          </div>
          <label class="flex cursor-pointer items-center gap-2 text-sm text-[#d4d4d4]">
            <input
              v-model="appState.aosConfig.enabled"
              type="checkbox"
              class="h-4 w-4 rounded border-[#404040] bg-[#171717] text-[#3b82f6]"
            />
            启用
          </label>
        </div>

        <div>
          <div class="mb-1 text-xs text-[#a3a3a3]">Leader 绑定模型适配器</div>
          <Select
            v-model="appState.aosConfig.leader.adapterID"
            :options="adapterOptions"
            placeholder="选择 Leader 适配器"
          />
        </div>

        <div class="flex items-center justify-between gap-2 border-t border-[#2a2a2a] pt-3">
          <h3 class="text-sm font-medium text-white">Members</h3>
          <div class="flex items-center gap-2">
            <Button
              variant="text"
              :disabled="recognizing"
              @click="handleRecognizeMembers"
            >
              {{ recognizing ? "认识中..." : "认识组员" }}
            </Button>
            <Button variant="text" @click="handleAddAOSMember">+ 添加成员</Button>
          </div>
        </div>
        <p
          v-if="!appState.aosRecognition || !appState.aosRecognition.members?.length"
          class="text-xs text-[#a3a3a3]"
        >
          还没认识组员。点击"认识组员"让 Leader 读每位成员的名称与提示词，自动生成路由标签 —
          之后正式开工时 Leader 只需读这些短标签即可调度，不用每次读完整提示词。
        </p>
        <div
          v-for="(member, idx) in appState.aosConfig.members"
          :key="idx"
          class="flex flex-col gap-3 rounded-md border border-[#2a2a2a] bg-[#171717] p-3"
        >
          <div class="flex items-center justify-between">
            <span class="text-xs text-[#a3a3a3]">成员 {{ idx + 1 }}</span>
            <button
              type="button"
              class="cursor-pointer text-xs text-[#ef4444] hover:underline"
              @click="handleRemoveAOSMember(idx)"
            >
              删除
            </button>
          </div>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <div class="mb-1 text-xs text-[#a3a3a3]">成员名称</div>
              <input
                v-model="member.name"
                type="text"
                placeholder="例如：研究员"
                class="h-9 w-full rounded-md border border-[#404040] bg-[#0f0f0f] px-3 text-sm text-white outline-none focus:border-[#3b82f6]"
              />
            </div>
            <div>
              <div class="mb-1 text-xs text-[#a3a3a3]">绑定模型适配器</div>
              <Select
                v-model="member.adapterID"
                :options="adapterOptions"
                placeholder="选择适配器"
              />
            </div>
          </div>
          <div>
            <div class="mb-1 text-xs text-[#a3a3a3]">System Prompt</div>
            <textarea
              v-model="member.systemPrompt"
              rows="3"
              placeholder="输入该成员的角色指令..."
              class="w-full rounded-md border border-[#404040] bg-[#0f0f0f] px-3 py-2 text-sm text-white outline-none focus:border-[#3b82f6]"
            />
          </div>
          <div>
            <div class="mb-1 text-xs text-[#a3a3a3]">
              Tags（由 Leader 在"认识组员"后自动生成，不可手动编辑）
            </div>
            <div v-if="tagsForMember(member.id)?.length" class="flex flex-wrap gap-1.5">
              <span
                v-for="tag in tagsForMember(member.id)"
                :key="tag"
                class="rounded-full bg-[#10AD5D] px-2.5 py-0.5 text-xs text-white"
              >
                {{ tag }}
              </span>
            </div>
            <p v-else class="text-xs text-[#737373]">
              尚未认识 — 点击上方"认识组员"自动生成
            </p>
          </div>
        </div>
        <p v-if="!appState.aosConfig.members.length" class="text-sm text-[#a3a3a3]">
          暂无成员，点击右上角"添加成员"开始配置。
        </p>
      </div>
    </Card>
  </div>
</template>
