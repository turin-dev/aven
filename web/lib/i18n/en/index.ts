// 그룹별 사전을 합친다. 같은 키가 여러 그룹에 있으면 뒤의 것이 이긴다(같은 원문은 같은 번역이어야 한다).
import g01 from "./g01";
import g02 from "./g02";
import g03 from "./g03";
import g04 from "./g04";
import g05 from "./g05";
import g06 from "./g06";
import g07 from "./g07";
import g08 from "./g08";
import g09 from "./g09";
import g10 from "./g10";
import g11 from "./g11";
import g12 from "./g12";
import g13 from "./g13";
import g14 from "./g14";
import common from "./common";
import ui_system from "./ov-ui-system";
import ui_canvas from "./ov-ui-canvas";
import ui_deployments from "./ov-ui-deployments";
import ui_vars_settings from "./ov-ui-vars-settings";
import ui_data from "./ov-ui-data";
import ui_networking from "./ov-ui-networking";
import ui_shell from "./ov-ui-shell";
import ui_auth from "./ov-ui-auth";
import ui_settings from "./ov-ui-settings";
import ui_admin from "./ov-ui-admin";
import ui_pages from "./ov-ui-pages";

export const EN: Record<string, string> = { ...common, ...g01, ...g02, ...g03, ...g04, ...g05, ...g06, ...g07, ...g08, ...g09, ...g10, ...g11, ...g12, ...g13, ...g14, ...ui_system, ...ui_canvas, ...ui_deployments, ...ui_vars_settings, ...ui_data, ...ui_networking, ...ui_shell, ...ui_auth, ...ui_settings, ...ui_admin, ...ui_pages };
