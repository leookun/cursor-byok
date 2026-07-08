const { addDynamicIconSelectors } = require("@iconify/tailwind");

const iconSafelist = [
  "icon-[ant-design--bilibili-outlined]",
  "icon-[bxl--openai]",
  "icon-[cil--badge]",
  "icon-[dashicons--yes]",
  "icon-[ic--round-close]",
  "icon-[ic--round-minus]",
  "icon-[logos--claude-icon]",
  "icon-[mdi--api]",
  "icon-[mdi--brain]",
  "icon-[mdi--check]",
  "icon-[mdi--chevron-down]",
  "icon-[mdi--content-copy]",
  "icon-[mdi--eye-off-outline]",
  "icon-[mdi--eye-outline]",
  "icon-[mdi--file-document-outline]",
  "icon-[mdi--head-cog-outline]",
  "icon-[mdi--head-lightbulb-outline]",
  "icon-[mdi--head-outline]",
  "icon-[mdi--information-outline]",
  "icon-[mdi--message-text-outline]",
  "icon-[mdi--pause]",
  "icon-[mdi--play]",
  "icon-[mdi--refresh]",
  "icon-[mdi--wifi]",
  "icon-[mingcute--loading-fill]",
];

module.exports = {
  content: ["./index.html", "./src/**/*.{vue,js,jsx,ts,tsx}"],
  safelist: [...iconSafelist, "z-999", "z-9999", "z-99999"],
  theme: {
    extend: {
      colors: {
        primary: {
          50: "#f0f7ff",
          100: "#e6f4ff",
          200: "#bae0ff",
          300: "#91caff",
          400: "#69b1ff",
          500: "#4096ff",
          600: "#1677ff",
          700: "#0958d9",
          800: "#003eb3",
          900: "#002c8c",
          950: "#001d66",
          DEFAULT: "#1677ff",
        },
      },
      fontFamily: {
        num: [
          "HFKos",
          "PingFang-Medium",
          "system-ui",
          "-apple-system",
          "BlinkMacSystemFont",
          "\"Segoe UI\"",
          "Roboto",
          "sans-serif",
        ],
      },
      fontSize: {
        xs: ["12px", { lineHeight: "16px" }],
        sm: ["13px", { lineHeight: "18px" }],
        lg: ["20px", { lineHeight: "28px" }],
      },
      zIndex: {
        999: "999",
        9999: "9999",
        99999: "99999",
      },
    },
  },
  plugins: [addDynamicIconSelectors()],
};
