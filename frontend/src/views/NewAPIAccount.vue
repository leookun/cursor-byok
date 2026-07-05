<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Input from "@/components/ui/Input.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import { Browser } from "@wailsio/runtime";
import {
  newAPITokenLogin,
  newAPIGetStatus,
  newAPILogout,
  newAPIOpenTopup,
} from "@/services/clientApi";
import { openNewAPILogsWindow, openNewAPIModelsWindow, reloadUserConfig, toUserError } from "@/state/appState";
import { onMounted, ref, computed } from "vue";

const message = useMessage();

async function openRelayStation() {
  try {
    await Browser.OpenURL("https://ymeng.cc");
  } catch (_error) {
    // 打开失败静默处理
  }
}

// --- 登录表单状态 ---
const tokenForm = ref({
  baseURL: "",
  token: "",
  userID: "",
  displayName: "",
});
const loginLoading = ref(false);

// --- 账号状态 ---
const accountStatus = ref({ loggedIn: false });
const statusLoading = ref(false);

const quotaDisplay = computed(() => {
  const usd = Number(accountStatus.value.quotaInUSD ?? 0);
  return `$${usd.toFixed(2)}`;
});

const usedQuotaDisplay = computed(() => {
  const used = Number(accountStatus.value.usedQuota ?? 0);
  return `$${(used / 500000).toFixed(2)}`;
});

async function fetchStatus() {
  statusLoading.value = true;
  try {
    accountStatus.value = await newAPIGetStatus();
  } catch (error) {
    console.error("[NewAPIAccount] 获取状态失败", error);
  } finally {
    statusLoading.value = false;
  }
}

async function handleTokenLogin() {
  const baseURL = tokenForm.value.baseURL.trim();
  const token = tokenForm.value.token.trim();
  const userID = tokenForm.value.userID.trim();
  const displayName = tokenForm.value.displayName.trim();

  if (!baseURL) {
    message.error("请填写 newapi 实例地址");
    return;
  }
  if (!token) {
    message.error("请填写个人令牌");
    return;
  }
  if (!userID) {
    message.error("请填写用户 ID");
    return;
  }

  loginLoading.value = true;
  try {
    const result = await newAPITokenLogin({ baseURL, token, userID, displayName });
    accountStatus.value = result;
    message.success(`已登录：${result.displayName || result.username}`);
    await reloadUserConfig().catch(() => {});
  } catch (error) {
    await showModal({
      title: "登录失败",
      content: toUserError(error),
    });
  } finally {
    loginLoading.value = false;
  }
}

async function handleLogout() {
  const confirmed = await showModal({
    title: "确认登出",
    content: "登出后将清除本地 token，需要重新登录才能使用 newapi 功能",
    confirmText: "登出",
    cancelText: "取消",
  });
  if (!confirmed) {
    return;
  }
  try {
    await newAPILogout();
    accountStatus.value = { loggedIn: false };
    tokenForm.value.baseURL = "";
    tokenForm.value.token = "";
    tokenForm.value.userID = "";
    tokenForm.value.displayName = "";
    message.success("已登出");
  } catch (error) {
    await showModal({
      title: "登出失败",
      content: toUserError(error),
    });
  }
}

async function handleTopup() {
  try {
    await newAPIOpenTopup();
  } catch (error) {
    await showModal({
      title: "打开充值页面失败",
      content: toUserError(error),
    });
  }
}

async function handleOpenModels() {
  try {
    await openNewAPIModelsWindow();
  } catch (error) {
    await showModal({
      title: "打开失败",
      content: toUserError(error),
    });
  }
}

async function handleOpenLogs() {
  try {
    await openNewAPILogsWindow();
  } catch (error) {
    await showModal({
      title: "打开失败",
      content: toUserError(error),
    });
  }
}

onMounted(() => {
  void fetchStatus();
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <!-- 加载中：避免已登录用户看到登录表单闪烁 -->
    <div v-if="statusLoading" class="flex flex-1 items-center justify-center text-sm text-[#a3a3a3]">
      加载中...
    </div>

    <!-- 未登录：登录表单 -->
    <Card v-else-if="!accountStatus.loggedIn">
      <div class="flex flex-col gap-4">
        <div>
          <h2 class="text-base font-medium text-white">NewAPI 账号绑定</h2>
          <div class="text-sm text-[#a3a3a3]">
            登录你的 newapi 中转站账号，同步模型、余额与使用记录
          </div>
        </div>

        <div class="flex items-center gap-1.5 text-xs text-[#8f8f8f]">
          <span>还没有中转站账号？</span>
          <button
            type="button"
            class="text-[#10AD5D] hover:text-[#29c776] underline-offset-2 hover:underline transition-colors"
            @click="openRelayStation"
          >前往 ymeng.cc 注册</button>
        </div>

        <div class="flex flex-col gap-3">
          <div class="flex flex-col gap-1">
            <label class="text-xs text-[#8f8f8f]">实例地址</label>
            <Input
              v-model="tokenForm.baseURL"
              placeholder="https://api.example.com"
            />
          </div>
          <div class="flex flex-col gap-1">
            <label class="text-xs text-[#8f8f8f]">个人令牌</label>
            <Input
              v-model="tokenForm.token"
              type="password"
              :allow-visibility-toggle="true"
              placeholder="从 NewAPI 后台获取的个人访问令牌"
              @keydown.enter="handleTokenLogin"
            />
          </div>
          <div class="flex flex-col gap-1">
            <label class="text-xs text-[#8f8f8f]">用户 ID</label>
            <Input
              v-model="tokenForm.userID"
              placeholder="NewAPI 后台「个人设置」可查看"
              @keydown.enter="handleTokenLogin"
            />
          </div>
          <div class="flex flex-col gap-1">
            <label class="text-xs text-[#8f8f8f]">显示名称（可选）</label>
            <Input
              v-model="tokenForm.displayName"
              placeholder="留空则使用实例地址"
              @keydown.enter="handleTokenLogin"
            />
          </div>
        </div>

        <Button
          variant="primary"
          :disabled="loginLoading"
          @click="handleTokenLogin"
        >
          {{ loginLoading ? "登录中..." : "登录" }}
        </Button>
      </div>
    </Card>

    <!-- 已登录：账号面板 -->
    <template v-else>
      <!-- 账号信息 -->
      <Card>
        <div class="flex items-center justify-between gap-4">
          <div class="flex flex-col gap-1">
            <div class="text-base font-medium text-white">
              {{ accountStatus.displayName || accountStatus.username || "NewAPI 用户" }}
            </div>
            <div class="text-xs text-[#8f8f8f]">{{ accountStatus.baseURL }}</div>
          </div>
          <Button variant="default" @click="handleLogout">登出</Button>
        </div>
      </Card>

      <!-- 余额 -->
      <Card>
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">钱包余额</h2>
            <div class="text-sm text-[#a3a3a3]">
              已使用 {{ usedQuotaDisplay }}
            </div>
          </div>
          <div class="flex items-center gap-4">
            <div class="text-2xl font-bold text-[#10AD5D]">{{ quotaDisplay }}</div>
            <Button variant="primary" @click="handleTopup">去充值</Button>
          </div>
        </div>
      </Card>

      <!-- 操作入口：模型导入 -->
      <Card>
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">模型导入</h2>
            <div class="text-sm text-[#a3a3a3]">
              从 newapi 拉取可用模型，勾选后导入为本地模型适配器
            </div>
          </div>
          <Button variant="primary" @click="handleOpenModels">
            导入模型
          </Button>
        </div>
      </Card>

      <!-- 操作入口：使用记录 -->
      <Card>
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">使用记录</h2>
            <div class="text-sm text-[#a3a3a3]">
              查看最近的模型调用记录与费用
            </div>
          </div>
          <Button variant="primary" @click="handleOpenLogs">
            查看记录
          </Button>
        </div>
      </Card>
    </template>
  </div>
</template>