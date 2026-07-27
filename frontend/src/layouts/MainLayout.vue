<script setup>
import { Browser, Window } from "@wailsio/runtime";
import LocaleSelect from "@/components/LocaleSelect.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  getFooterAuthorInfo,
  openFooterAuthorHome,
} from "@/services/clientApi";
import {
  appState,
  checkForAppUpdates,
  syncServiceState,
  updateViewState,
} from "@/state/appState";
import { isWindows } from "@/utils/isWindows";
import { computed, onMounted, onUnmounted, ref } from "vue";
import { useRoute } from "vue-router";
import Logo from "@/assets/logo.png";

const route = useRoute();
const message = useMessage();
const showIcon = computed(() => route.meta.showIcon !== false);
const title = computed(() => route.meta.title ?? "Cursor助手｜永久免费｜自定义API");
const directlyClose = computed(() => route.meta.directlyClose === true);
const showFooter = computed(() => route.path === "/");
const footerAuthorInfo = ref(null);

const localizedAuthorInfo = computed(() => {
  if (!footerAuthorInfo.value) return null;
  return {
    buttonText: "作者 leookun",
    dialogTitle: "作者寄语",
    dialogContent: "本软件是纯免费软件，如果你被收费，那大概率就是被骗了。\n欢迎点击访问作者主页 https://space.bilibili.com/311706663/upload/video\n查看更多更新动态、使用分享和后续内容。",
    dialogConfirmText: "访问主页",
    dialogCancelText: "关闭",
  };
});
const usageDocsURL = "https://docs.leokun.cn";
let proxyStateTimer = null;
const proxyStatePollIntervalMs = 10000;
const netProxyEndpoint = computed(
  () => appState.netProxyHttps || appState.netProxyHttp || "",
);
const proxyBadgeText = computed(() => {
  if (appState.netProxyUsingSystem) {
    return "已识别系统代理";
  }
  return "";
});
const proxyBadgeTitle = computed(() => {
  if (appState.netProxyUsingSystem) {
    return netProxyEndpoint.value
      ? `当前出站请求使用系统代理：${netProxyEndpoint.value}`
      : "当前出站请求使用系统代理";
  }
  if (appState.netProxyUsingEnv) {
    return netProxyEndpoint.value
      ? `当前出站请求使用环境变量代理：${netProxyEndpoint.value}`
      : "当前出站请求使用环境变量代理";
  }
  if (appState.netProxyPacIgnored) {
    return "检测到系统 PAC/自动代理，当前版本按直连处理";
  }
  return "当前出站请求未使用系统代理";
});

async function minimizeWindow() {
  await Window.Minimise();
}

async function closeWindow() {
  if (directlyClose.value) {
    await Window.Close();
    return;
  }
  // const confirmed = await showModal({
  //   title: "确认关闭",
  //   content: "程序将会最小化到托盘，彻底关闭请在托盘退出，关闭后无法使用Cursor",
  // });
  // if (!confirmed) {
  //   return;
  // }
  await new Promise((resolve) => setTimeout(resolve, 200));
  await Window.Hide();
}

async function handleCheckForUpdates() {
  if (updateViewState.footerBusy || updateViewState.footerDownloading) {
    return;
  }
  const loadingMessageID = message.loading("检查更新中...");
  try {
    await checkForAppUpdates();
  } finally {
    if (loadingMessageID) {
      message.remove(loadingMessageID);
    }
  }
}

async function loadFooterAuthorInfo() {
  try {
    footerAuthorInfo.value = await getFooterAuthorInfo();
  } catch (error) {
    console.error("[MainLayout] 加载作者信息失败", error);
  }
}

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || "操作失败").trim() || "操作失败",
    confirmText: "确定",
    showCancel: false,
  });
}

async function handleOpenAuthorHome() {
  if (!localizedAuthorInfo.value) {
    return;
  }
  const confirmed = await showModal({
    title: localizedAuthorInfo.value.dialogTitle,
    content: localizedAuthorInfo.value.dialogContent,
    confirmText: localizedAuthorInfo.value.dialogConfirmText,
    cancelText: localizedAuthorInfo.value.dialogCancelText,
    showCancel: true,
  });
  if (!confirmed) {
    return;
  }
  try {
    await openFooterAuthorHome();
  } catch (error) {
    await showActionError("打开主页失败", error);
  }
}

async function handleOpenUsageDocs() {
  try {
    await Browser.OpenURL(usageDocsURL);
  } catch (error) {
    await showActionError("打开使用教程失败", error);
  }
}

onMounted(() => {
  void loadFooterAuthorInfo();
  proxyStateTimer = window.setInterval(() => {
    if (showFooter.value) {
      void syncServiceState().catch(() => {});
    }
  }, proxyStatePollIntervalMs);
});

onUnmounted(() => {
  if (proxyStateTimer) {
    window.clearInterval(proxyStateTimer);
    proxyStateTimer = null;
  }
});
</script>

<template>
  <div class="flex h-screen w-screen overflow-hidden flex-col">
    <div
      class="fixed top-0 w-screen h-[40px] z-9999 w-full"
      style="--wails-draggable: drag"
    ></div>

    <header
      class="flex h-[40px] center-row px-[20px] w-full min-h-0 shrink-0 justify-between relative"
      style="--wails-draggable: drag"
      :class="{ '!justify-center': !isWindows }"
    >
      <div class="center-row gap-2" style="font-family: var(--font-num);">
        <img v-if="showIcon" :src="Logo" class="w-[18px] h-[18px]" />
        <div>{{ title }}</div>
      </div>
      <div
        v-if="isWindows"
        class="absolute right-[10px] top-[8px] z-99999 center-row gap-[1px]"
      >
        <button
          class="text-[20px] center-row justify-center w-[30px] h-[23px] rounded-[4px] text-[#777] hover:bg-[#333] hover:text-[#ddd] cursor-pointer"
          @click="minimizeWindow"
        >
          <span class="icon-[ic--round-minus]"></span>
        </button>
        <button
          class="text-[20px] center-row justify-center w-[30px] h-[23px] rounded-[4px] text-[#777] hover:bg-[#333] hover:text-[#ddd] cursor-pointer"
          @click="closeWindow"
        >
          <span class="icon-[ic--round-close]"></span>
        </button>
      </div>
    </header>

    <main class="flex-1 min-h-0 overflow-hidden flex flex-col w-full">
      <router-view />
    </main>

    <footer
      v-if="showFooter"
      class="flex !pr-1 h-[30px] shrink-0 items-center gap-[8px] border-t border-[#242424] px-[14px] text-[12px] text-[#8f8f8f]"
    >
      <div
        v-if="proxyBadgeText"
        class="center-row  border-none gap-[2px]  border-none  px-[0px] py-[3px] leading-none "
        aria-live="polite"
      >
        <span class="icon-[mdi--wifi] text-[15px]"></span>
        <span class="truncate">{{ proxyBadgeText }}</span>
      </div>
      <button
        v-if="!updateViewState.footerDownloading"
        type="button"
        class="center-row shrink-0 gap-[6px] cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[#1f1f1f] hover:text-[#e5e5e5]"
        :disabled="updateViewState.footerBusy"
        @click="handleCheckForUpdates"
      >
        <span>{{ updateViewState.footerVersionLabel }}</span>
        <span>检查更新</span>
      </button>
      <button
        type="button"
        class="center-row shrink-0 gap-[2px]  cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[#1f1f1f] hover:text-[#e5e5e5]"
        @click="handleOpenUsageDocs"
      >
        <span class="icon-[mdi--file-document-outline] text-[15px]"></span>
        <span>使用教程</span>
      </button>
      <button
        v-if="localizedAuthorInfo"
        type="button"
        class="center-row shrink-0 gap-[6px] cursor-pointer rounded-[6px] px-[6px] py-[3px] transition-colors duration-150 hover:bg-[#1f1f1f] hover:text-[#e5e5e5]"
        @click="handleOpenAuthorHome"
      >
        <span class="icon-[ant-design--bilibili-outlined] text-[14px]"></span>
        <span>{{ localizedAuthorInfo.buttonText }}</span>
      </button>
      <div
        v-if="updateViewState.footerDownloading"
        class="flex min-w-0 flex-1 items-center gap-[10px]"
      >
        <span class="shrink-0">{{ updateViewState.footerVersionLabel }}</span>
        <div class="center-row min-w-0 gap-[8px]">
          <div
            class="h-[6px] w-[120px] overflow-hidden rounded-full bg-[#1f1f1f]"
          >
            <div
              class="h-full rounded-full bg-gradient-to-r from-[#10AD5D] to-[#29c776]"
              :style="updateViewState.footerProgressStyle"
            ></div>
          </div>
          <span class="shrink-0 text-[#d4d4d4]">{{
            updateViewState.footerProgressText
          }}</span>
        </div>
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-[8px]">
        <LocaleSelect
          :border="false"
          aria-label="界面语言"
          wrapper-class="w-auto"
          button-class="h-[24px] bg-transparent px-1.5 text-[12px] !text-[#8f8f8f] !hover:text-[#e5e5e5]"
          menu-class="text-[12px]"
        />
      </div>
    </footer>
  </div>
</template>
