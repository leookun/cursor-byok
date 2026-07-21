import { createRouter, createWebHashHistory } from "vue-router";
import Home from "@/views/Home.vue";
import Config from "@/views/Config.vue";
import ModelConfig from "@/views/ModelConfig.vue";
import ModelEditor from "@/views/ModelEditor.vue";
import VirtualModels from "@/views/VirtualModels.vue";
import ToolManagement from "@/views/ToolManagement.vue";
import CacheDashboard from "@/views/CacheDashboard.vue";
import TelemetryDashboard from "@/views/TelemetryDashboard.vue";
import Plugins from "@/views/Plugins.vue";
import WorkflowEditor from "@/views/WorkflowEditor.vue";
import NotFound from "@/views/NotFound.vue";

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: "/",
      name: "home",
      component: Home,
      meta: { showIcon: true, title: "Cursor助手｜永久免费｜自定义API" },
    },
    {
      path: "/config",
      name: "config",
      component: Config,
      meta: { showIcon: true, title: "设置" },
    },
    {
      path: "/model-config",
      name: "modelConfig",
      component: ModelConfig,
      meta: { showIcon: false, title: "模型配置" },
    },
    {
      path: "/model-editor",
      name: "modelEditor",
      component: ModelEditor,
      meta: { showIcon: false, title: "模型编辑" },
    },
    {
      path: "/virtual-models",
      name: "virtualModels",
      component: VirtualModels,
      meta: { showIcon: false, title: "虚拟模型" },
    },
    {
      path: "/tool-management",
      name: "toolManagement",
      component: ToolManagement,
      meta: { showIcon: false, title: "工具管理" },
    },
    {
      path: "/cache-dashboard",
      name: "cacheDashboard",
      component: CacheDashboard,
      meta: { showIcon: false, title: "缓存 Dashboard" },
    },
    {
      path: "/telemetry-dashboard",
      name: "telemetryDashboard",
      component: TelemetryDashboard,
      meta: { showIcon: false, title: "Telemetry Dashboard" },
    },
    {
      path: "/plugins",
      name: "plugins",
      component: Plugins,
      meta: { showIcon: false, title: "插件" },
    },
    {
      path: "/workflows",
      name: "workflows",
      component: WorkflowEditor,
      meta: { showIcon: false, title: "工作流编辑器" },
    },
    {
      path: "/:pathMatch(.*)*",
      name: "notFound",
      component: NotFound,
      meta: { showIcon: false, title: "页面未找到" },
    },
  ],
});

// 全局路由守卫：更新页面标题
router.beforeEach((to) => {
  if (to.meta.title) {
    document.title = to.meta.title;
  }
});

export default router;
