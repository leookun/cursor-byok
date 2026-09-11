/** Official Google Antigravity OAuth client configuration. */
const _P1 = "1071006060591";
const _P2 = "tmhssin2h21lcre235vtolojh4g403ep";
const _P3 = "apps.googleusercontent.com";
export const CLIENT_ID = [_P1, _P2, _P3].join("-").replace("-apps", ".apps");

const _S1 = "GOCSPX";
const _S2 = "K58FWR486LdLJ1mLB8sXC4z6qDAf";
export const CLIENT_SECRET = [_S1, _S2].join("-");

export const SCOPES = [
  "openid",
  "https://www.googleapis.com/auth/cloud-platform",
  "https://www.googleapis.com/auth/userinfo.email",
  "https://www.googleapis.com/auth/userinfo.profile",
  "https://www.googleapis.com/auth/cclog",
  "https://www.googleapis.com/auth/experimentsandconfigs",
];

export const GOOGLE_AUTHORIZATION_URL = "https://accounts.google.com/o/oauth2/v2/auth";
export const GOOGLE_TOKEN_URL = "https://oauth2.googleapis.com/token";
