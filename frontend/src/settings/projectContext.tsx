import { createContext } from "react";

import type { Project } from "../api/generated/types.gen";
import type { SettingsSectionId } from "./registry";

export const SettingsProjectContext = createContext<Project | undefined>(undefined);
export const SettingsNavContext = createContext<((section: SettingsSectionId) => void) | undefined>(undefined);
