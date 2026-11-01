"use client";

import { forwardRef, useId, useMemo, useState, type ComponentProps, type KeyboardEvent, type ReactNode } from "react";
import { ArrowBigUp, Eye, EyeOff } from "lucide-react";
import { passwordStrength, type StrengthHint } from "@/lib/passwordStrength";
import { t } from "@/lib/i18n";
import { AvenMark, Input, cx } from "@/components/ui";

/* Building blocks shared by the sign-in, invite, OAuth consent and account screens. */

/**
 * Full-height page for signed-out screens: dot grid that fades out towards the edges and a soft
 * accent glow behind the card. Both layers are decorative and static (nothing to reduce for
 * prefers-reduced-motion).
 */
export function AuthShell({ children, footer, wide }: { children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  return (
    <main className="relative isolate flex min-h-dvh flex-col items-center justify-center overflow-hidden bg-background px-4 py-10">
      <div
        aria-hidden
        className="dot-grid pointer-events-none absolute inset-0 -z-10 opacity-70 [mask-image:radial-gradient(ellipse_at_center,black_10%,transparent_65%)]"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute left-1/2 top-1/2 -z-10 h-[460px] w-[min(760px,140vw)] -translate-x-1/2 -translate-y-[60%] rounded-full opacity-[0.18] blur-3xl dark:opacity-25"
        style={{ background: "radial-gradient(closest-side, var(--color-pink-500), transparent)" }}
      />
      <div
        className={cx(
          "w-full rounded-xl border border-gray-200 bg-background/90 shadow-[0_12px_48px_-12px_rgba(0,0,0,0.25)] backdrop-blur-sm",
          wide ? "max-w-[519px]" : "max-w-[400px]",
        )}
      >
        {children}
      </div>
      {footer && <div className="mt-4 flex items-center justify-center gap-3 text-xs text-gray-500">{footer}</div>}
    </main>
  );
}

/** Brand mark, title and subtitle at the top of an AuthShell card. */
export function AuthHeader({ title, subtitle, mark }: { title: ReactNode; subtitle?: ReactNode; mark?: ReactNode }) {
  return (
    <div className="flex flex-col items-center px-8 pb-6 pt-9 text-center">
      {mark ?? <AvenMark className="size-12 drop-shadow-[0_6px_18px_rgba(135,59,206,0.45)]" />}
      <h1 className="mt-5 text-lg font-semibold leading-7">{title}</h1>
      {subtitle && <p className="mt-1 text-sm text-gray-500">{subtitle}</p>}
    </div>
  );
}

/** Horizontal rule with a short label ("또는") between sign-in methods. */
export function OrDivider() {
  return (
    <div className="flex items-center gap-3 text-xs text-gray-500" role="separator" aria-label={t("또는")}>
      <span className="h-px flex-1 bg-gray-200" />
      {t("또는")}
      <span className="h-px flex-1 bg-gray-200" />
    </div>
  );
}

type PasswordInputProps = Omit<ComponentProps<typeof Input>, "type">;

/**
 * Password field with a show/hide toggle and a Caps Lock warning. The toggle keeps the input's
 * autocomplete attribute, so password managers still recognise the field.
 */
export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(function PasswordInput(
  { className, onKeyDown, onKeyUp, onBlur, ...rest },
  ref,
) {
  const [shown, setShown] = useState(false);
  const [caps, setCaps] = useState(false);
  const capsId = useId();
  const readCaps = (e: KeyboardEvent<HTMLInputElement>) => setCaps(e.getModifierState?.("CapsLock") ?? false);
  return (
    <span className="block">
      <span className="relative block">
        <Input
          ref={ref}
          {...rest}
          type={shown ? "text" : "password"}
          autoCapitalize="none"
          autoCorrect="off"
          aria-describedby={cx(rest["aria-describedby"], caps && capsId) || undefined}
          className={cx("pr-11", className)}
          onKeyDown={(e) => (readCaps(e), onKeyDown?.(e))}
          onKeyUp={(e) => (readCaps(e), onKeyUp?.(e))}
          onBlur={(e) => (setCaps(false), onBlur?.(e))}
        />
        <button
          type="button"
          onClick={() => setShown((s) => !s)}
          aria-label={shown ? t("비밀번호 숨기기") : t("비밀번호 보기")}
          aria-pressed={shown}
          title={shown ? t("비밀번호 숨기기") : t("비밀번호 보기")}
          className="absolute inset-y-1 right-1 z-20 flex w-9 cursor-pointer items-center justify-center rounded text-gray-500 hover:bg-gray-100 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500"
        >
          {shown ? <EyeOff className="size-4" aria-hidden /> : <Eye className="size-4" aria-hidden />}
        </button>
      </span>
      {caps && (
        <span id={capsId} role="status" className="mt-1.5 flex items-center gap-1.5 text-xs text-warn">
          <ArrowBigUp className="size-3.5 shrink-0" aria-hidden />
          {t("Caps Lock이 켜져 있습니다.")}
        </span>
      )}
    </span>
  );
});

const STRENGTH_COLORS = ["bg-red-500", "bg-red-500", "bg-yellow-500", "bg-green-500", "bg-green-500"];

/**
 * Four-segment strength meter under a new-password field. Advisory only: the server accepts any
 * non-empty password, so this never blocks submission. `personal` = the user's name and email.
 */
export function PasswordStrengthMeter({ password, personal = [], id }: { password: string; personal?: string[]; id?: string }) {
  const key = personal.join("\u0000");
  const strength = useMemo(() => passwordStrength(password, key ? key.split("\u0000") : []), [password, key]);
  if (!password) return null;
  const labels = [t("매우 약함"), t("약함"), t("보통"), t("강함"), t("매우 강함")];
  const hint: Record<StrengthHint, string> = {
    short: t("8자 이상으로 늘리세요."),
    common: t("흔히 쓰는 비밀번호입니다."),
    personal: t("이름이나 이메일이 들어 있습니다."),
    repeat: t("같은 문자를 반복하지 마세요."),
    sequence: t("abc, 123, qwerty 같은 연속된 문자는 추측하기 쉽습니다."),
    variety: t("단어를 몇 개 이어 붙이거나 숫자와 기호를 섞으세요."),
  };
  const first = strength.hints[0];
  return (
    <span id={id} className="mt-2 block" aria-live="polite">
      <span className="flex gap-1" aria-hidden>
        {[1, 2, 3, 4].map((i) => (
          <span
            key={i}
            className={cx(
              "h-1 flex-1 rounded-full transition-colors motion-reduce:transition-none",
              strength.score >= i || (i === 1 && strength.score === 0) ? STRENGTH_COLORS[strength.score] : "bg-gray-200",
            )}
          />
        ))}
      </span>
      <span className="mt-1.5 flex flex-wrap justify-between gap-x-3 text-xs text-gray-600">
        <span>{t("비밀번호 강도: {level}", { level: labels[strength.score] })}</span>
        {first && <span className="text-gray-500">{hint[first]}</span>}
      </span>
    </span>
  );
}
