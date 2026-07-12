import { createRouter, createWebHashHistory } from "vue-router";
import Home from "@/views/Home.vue";
import Config from "@/views/Config.vue";
import ModelConfig from "@/views/ModelConfig.vue";
import ModelEditor from "@/views/ModelEditor.vue";
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
