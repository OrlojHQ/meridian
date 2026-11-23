import { createContext } from "react";

import type { Project } from "../api/generated/types.gen";

export const SettingsProjectContext = createContext<Project | undefined>(undefined);
