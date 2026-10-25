import type { WebAuthnOptions } from "./api";
import { t } from "@/lib/i18n";

type CredentialDescriptorJSON = { type: "public-key"; id: string; transports?: AuthenticatorTransport[] };
type CreationOptionsJSON = {
  challenge: string;
  user: { id: string; [key: string]: unknown };
  excludeCredentials?: CredentialDescriptorJSON[];
  [key: string]: unknown;
};
type RequestOptionsJSON = {
  challenge: string;
  allowCredentials?: CredentialDescriptorJSON[];
  [key: string]: unknown;
};

function decodeBase64URL(value: string): ArrayBuffer {
  const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(normalized + "=".repeat((4 - (normalized.length % 4)) % 4));
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
  return bytes.buffer as ArrayBuffer;
}

function encodeBase64URL(value: ArrayBuffer): string {
  const bytes = new Uint8Array(value);
  let binary = "";
  for (let i = 0; i < bytes.length; i += 1) binary += String.fromCharCode(bytes[i]);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

/** WebAuthn은 보안 컨텍스트(HTTPS 또는 localhost)와 PublicKeyCredential을 지원하는 브라우저에서만 쓸 수 있다. */
export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && window.isSecureContext && typeof PublicKeyCredential !== "undefined" && !!navigator.credentials;
}

function ensureWebAuthnAvailable(): void {
  if (typeof window !== "undefined" && !window.isSecureContext) {
    throw new Error(t("패스키는 HTTPS 또는 localhost에서만 쓸 수 있습니다."));
  }
  if (!passkeysSupported()) {
    throw new Error(t("이 브라우저는 패스키를 지원하지 않습니다."));
  }
}

/** 브라우저의 DOMException을 사용자에게 보여 줄 문장으로 바꾼다. */
function friendlyError(err: unknown): never {
  const name = err instanceof DOMException ? err.name : "";
  if (name === "NotAllowedError" || name === "AbortError") throw new Error(t("패스키 요청이 취소되었거나 시간이 지났습니다."));
  if (name === "InvalidStateError") throw new Error(t("이 인증기에는 이미 이 계정의 패스키가 있습니다."));
  if (name === "SecurityError") throw new Error(t("이 주소에서는 패스키를 쓸 수 없습니다(도메인 이름과 HTTPS가 필요합니다)."));
  throw err;
}

function creationOptions(input: WebAuthnOptions): PublicKeyCredentialCreationOptions {
  const source = input.publicKey as CreationOptionsJSON;
  return {
    ...source,
    challenge: decodeBase64URL(source.challenge),
    user: { ...source.user, id: decodeBase64URL(source.user.id) },
    excludeCredentials: source.excludeCredentials?.map((item) => ({ ...item, id: decodeBase64URL(item.id) })),
  } as PublicKeyCredentialCreationOptions;
}

function requestOptions(input: WebAuthnOptions): PublicKeyCredentialRequestOptions {
  const source = input.publicKey as RequestOptionsJSON;
  return {
    ...source,
    challenge: decodeBase64URL(source.challenge),
    allowCredentials: source.allowCredentials?.map((item) => ({ ...item, id: decodeBase64URL(item.id) })),
  } as PublicKeyCredentialRequestOptions;
}

export async function createPasskey(options: WebAuthnOptions): Promise<Record<string, unknown>> {
  ensureWebAuthnAvailable();
  const result = await navigator.credentials.create({ publicKey: creationOptions(options) }).catch(friendlyError);
  if (!(result instanceof PublicKeyCredential)) throw new Error(t("패스키 등록이 취소되었거나 완료되지 않았습니다."));
  const response = result.response as AuthenticatorAttestationResponse;
  return {
    id: result.id,
    rawId: encodeBase64URL(result.rawId),
    type: result.type,
    response: {
      clientDataJSON: encodeBase64URL(response.clientDataJSON),
      attestationObject: encodeBase64URL(response.attestationObject),
      transports: response.getTransports?.(),
    },
    clientExtensionResults: result.getClientExtensionResults(),
    authenticatorAttachment: result.authenticatorAttachment,
  };
}

export async function getPasskey(options: WebAuthnOptions): Promise<Record<string, unknown>> {
  ensureWebAuthnAvailable();
  const result = await navigator.credentials.get({ publicKey: requestOptions(options) }).catch(friendlyError);
  if (!(result instanceof PublicKeyCredential)) throw new Error(t("패스키 인증이 취소되었거나 완료되지 않았습니다."));
  const response = result.response as AuthenticatorAssertionResponse;
  return {
    id: result.id,
    rawId: encodeBase64URL(result.rawId),
    type: result.type,
    response: {
      clientDataJSON: encodeBase64URL(response.clientDataJSON),
      authenticatorData: encodeBase64URL(response.authenticatorData),
      signature: encodeBase64URL(response.signature),
      userHandle: response.userHandle ? encodeBase64URL(response.userHandle) : null,
    },
    clientExtensionResults: result.getClientExtensionResults(),
    authenticatorAttachment: result.authenticatorAttachment,
  };
}
