"use client";

/* Target-port picker, modeled on Railway's dashboard: a full-width dropdown trigger (`h-md`, "Custom port" / the chosen port + chevron) listing
   the ports keel detected in the container, then — for "직접 입력" — a port prompt and the
   number input (`data-testid="port-selector-input"`, placeholder 8080). */

import { createContext, useContext } from "react";
import { ChevronDown } from "lucide-react";
import type { DetectedPort, DetectedPortSource, PortDetection } from "@/lib/api";
import { t } from "@/lib/i18n";
import { Menu, MenuItem, cx } from "../../ui";
import { INPUT, SR_LABEL } from "./parts";

const SOURCE: Record<DetectedPortSource | "service", string> = {
  service: t("서비스 포트"),
  listening: "listening",
  expose: "EXPOSE",
  env: t("PORT 변수"),
  loopback: t("127.0.0.1 전용"),
};

/** Port detection status of the instance (P4), provided once by the networking section. */
export const PortDetectionContext = createContext<PortDetection | undefined>(undefined);

/** Picker state: a detected port, or free text in "직접 입력" mode. */
export type PortChoice = { mode: "pick" | "custom"; text: string };

/** Initial state for a current port: pick it when it's one of the candidates, otherwise open "직접 입력" with it filled in. */
export function initialPortChoice(port: number | null | undefined, detected: DetectedPort[]): PortChoice {
  if (port != null && detected.some((d) => d.port === port && d.source !== "loopback")) return { mode: "pick", text: String(port) };
  return { mode: "custom", text: port != null ? String(port) : "" };
}

export function PortPicker({
  value,
  onChange,
  detected,
  servicePort,
  id,
}: {
  value: PortChoice;
  onChange: (v: PortChoice) => void;
  detected: DetectedPort[];
  /** Effective service port; offered as a candidate when it wasn't detected. */
  servicePort?: number;
  id: string;
}) {
  const detection = useContext(PortDetectionContext);
  const options: { port: number; source: DetectedPortSource | "service" }[] = [...detected];
  if (servicePort && !options.some((d) => d.port === servicePort)) options.unshift({ port: servicePort, source: "service" });
  return (
    <>
      <Menu
        label={t("대상 포트")}
        align="start"
        trigger={(tg) => (
          <button
            type="button"
            id={`radix-${id}`}
            {...tg}
            className="flex h-md w-full cursor-pointer items-center space-x-2 rounded-md border border-gray-200 px-3 py-2 hover:border-gray-400 focus:outline-none focus-visible:border-pink-400"
          >
            {value.mode === "custom" ? t("직접 입력") : value.text}
            <span className="icon-container icon-md ml-2 text-2xl text-gray-500" aria-hidden="true">
              <ChevronDown />
            </span>
          </button>
        )}
      >
        {(close) => (
          <>
            {options.length > 0 && <div className="px-3 py-1 text-xs font-medium text-gray-600">{t("감지된 포트")}</div>}
            {detection?.source === "unavailable" && (
              <p className="max-w-[300px] px-3 py-1 text-xs leading-[18px] text-gray-500">
                {t("listen 중인 포트를 읽지 못했습니다")}{detection.reason ? `: ${detection.reason}` : ""}
              </p>
            )}
            {options.map((d) => (
              <MenuItem
                key={d.port}
                disabled={d.source === "loopback"}
                reason={d.source === "loopback" ? t("127.0.0.1에서만 listen하고 있어 프록시가 닿지 않습니다. 앱이 0.0.0.0이나 ::에서 listen하도록 바꾸세요.") : undefined}
                onSelect={() => (onChange({ mode: "pick", text: String(d.port) }), close())}
              >
                <span className="font-mono">{d.port}</span>
                <span className="mx-1.5 text-gray-400">·</span>
                <span className={cx(d.source === "listening" ? "text-green-600" : "text-gray-500")}>{SOURCE[d.source]}</span>
              </MenuItem>
            ))}
            {options.length > 0 && <div className="my-2 h-px w-full bg-gray-100" />}
            <MenuItem onSelect={() => (onChange({ mode: "custom", text: value.text }), close())}>{t("직접 입력")}</MenuItem>
          </>
        )}
      </Menu>
      {value.mode === "custom" && (
        <>
          <p className="text-sm text-gray-700">{t("앱이 listen하는 포트를 입력하세요")}</p>
          <div className="flex w-full flex-col">
            <div className="flex items-center justify-between">
              <div className="flex space-x-1">
                <label className={SR_LABEL} htmlFor={`input-port-${id}`}>
                  {t("대상 포트")}
                </label>
              </div>
            </div>
            <div className="relative flex w-full items-center">
              <input
                id={`input-port-${id}`}
                data-testid="port-selector-input"
                placeholder="8080"
                type="number"
                min={1}
                max={65535}
                value={value.text}
                onChange={(e) => onChange({ mode: "custom", text: e.target.value.replace(/[^0-9]/g, "").slice(0, 5) })}
                className={INPUT}
              />
            </div>
          </div>
        </>
      )}
    </>
  );
}
