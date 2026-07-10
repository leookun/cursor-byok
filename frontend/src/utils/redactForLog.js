const SENSITIVE_LOG_KEY = /^(?:api[-_]?key|key|authorization|proxy-authorization|token|accessToken|refreshToken|secret|password|customHeadersJSON|adapterJSON)$/i;

export function redactForLog(value, seen = new WeakSet()) {
  if (value === null || typeof value !== "object") {
    return value;
  }
  if (value instanceof Error) {
    return {
      name: value.name,
      message: value.message,
    };
  }
  if (seen.has(value)) {
    return "[Circular]";
  }
  seen.add(value);
  if (Array.isArray(value)) {
    return value.map((item) => redactForLog(item, seen));
  }
  return Object.fromEntries(
    Object.entries(value).map(([key, item]) => [
      key,
      SENSITIVE_LOG_KEY.test(key) ? "[REDACTED]" : redactForLog(item, seen),
    ]),
  );
}
