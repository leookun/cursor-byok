/**
 * Shared type casting utilities used across components.
 */

/**
 * Safely cast a value to string, returning "" for undefined/null/non-stringable values.
 */
export function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (value instanceof String) {
    return value.toString().trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

/**
 * Safely cast a value to boolean.
 * @param {*} value
 * @param {boolean} [fallback=false]
 * @returns {boolean}
 */
export function asBoolean(value, fallback = false) {
  if (typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    return value !== 0;
  }
  const normalized = asString(value).toLowerCase();
  if (!normalized) {
    return fallback;
  }
  return normalized === "true" || normalized === "1" || normalized === "yes";
}

/**
 * Safely cast a value to number.
 * @param {*} value
 * @param {number} [fallback=0]
 * @returns {number}
 */
export function asNumber(value, fallback = 0) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  const text = asString(value);
  if (!text) {
    return fallback;
  }
  const parsed = Number(text);
  return Number.isFinite(parsed) ? parsed : fallback;
}

/**
 * Safely cast a value to an array.
 * @param {*} value
 * @returns {Array}
 */
export function asArray(value) {
  return Array.isArray(value) ? value : [];
}

/**
 * Safely cast a value to a positive integer string, returning "" for invalid inputs.
 * @param {*} value
 * @returns {string}
 */
export function asPositiveIntegerString(value) {
  const text = asString(value);
  if (!text) {
    return "";
  }
  if (!/^\d+$/.test(text)) {
    return "";
  }
  return Number(text) > 0 ? text : "";
}

/**
 * Safely cast a value to a positive integer, returning 0 for invalid inputs.
 * @param {*} value
 * @returns {number}
 */
export function asPositiveInteger(value) {
  const text = asPositiveIntegerString(value);
  if (!text) {
    return 0;
  }
  return Number(text);
}

/**
 * Cast a value to a finite number or null.
 * @param {*} value
 * @returns {number|null}
 */
export function asNullableRate(value) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return null;
}
