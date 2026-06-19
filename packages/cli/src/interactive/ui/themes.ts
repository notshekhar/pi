/**
 * Built-in theme palettes — copied from dark.json / light.json so
 * our rendering stays consistent. Embedded as TS so no JSON assets need to
 * ship next to the compiled binary.
 */

export interface ThemeColors {
    accent: string | number;
    border: string | number;
    borderAccent: string | number;
    borderMuted: string | number;
    success: string | number;
    error: string | number;
    warning: string | number;
    muted: string | number;
    dim: string | number;
    text: string | number;
    thinkingText: string | number;
    selectedBg: string | number;
    userMessageBg: string | number;
    userMessageText: string | number;
    customMessageBg: string | number;
    customMessageText: string | number;
    customMessageLabel: string | number;
    toolPendingBg: string | number;
    toolSuccessBg: string | number;
    toolErrorBg: string | number;
    toolTitle: string | number;
    toolOutput: string | number;
    /** Failed tool title/output — vivid, unlike the muted `error`/diff red. */
    toolError: string | number;
    mdHeading: string | number;
    mdLink: string | number;
    mdLinkUrl: string | number;
    mdCode: string | number;
    mdCodeBlock: string | number;
    mdCodeBlockBorder: string | number;
    mdQuote: string | number;
    mdQuoteBorder: string | number;
    mdHr: string | number;
    mdListBullet: string | number;
    toolDiffAdded: string | number;
    toolDiffRemoved: string | number;
    toolDiffContext: string | number;
    syntaxComment: string | number;
    syntaxKeyword: string | number;
    syntaxFunction: string | number;
    syntaxVariable: string | number;
    syntaxString: string | number;
    syntaxNumber: string | number;
    syntaxType: string | number;
    syntaxOperator: string | number;
    syntaxPunctuation: string | number;
    thinkingOff: string | number;
    thinkingMinimal: string | number;
    thinkingLow: string | number;
    thinkingMedium: string | number;
    thinkingHigh: string | number;
    thinkingXhigh: string | number;
    bashMode: string | number;
}

export interface ThemeJson {
    name: string;
    vars?: Record<string, string | number>;
    colors: ThemeColors;
}

export const DARK_THEME: ThemeJson = {
    name: "dark",
    vars: {
        cyan: "#00d7ff",
        blue: "#5f87ff",
        green: "#b5bd68",
        red: "#cc6666",
        yellow: "#ffff00",
        text: "#d4d4d4",
        gray: "#808080",
        dimGray: "#666666",
        darkGray: "#505050",
        accent: "#8abeb7",
        selectedBg: "#3a3a4a",
        userMsgBg: "#343541",
        toolPendingBg: "#282832",
        toolSuccessBg: "#283228",
        toolErrorBg: "#3c2828",
        customMsgBg: "#2d2838",
    },
    colors: {
        accent: "accent",
        border: "blue",
        borderAccent: "cyan",
        borderMuted: "darkGray",
        success: "green",
        error: "red",
        warning: "yellow",
        muted: "gray",
        dim: "dimGray",
        text: "text",
        thinkingText: "gray",
        selectedBg: "selectedBg",
        userMessageBg: "userMsgBg",
        userMessageText: "text",
        customMessageBg: "customMsgBg",
        customMessageText: "text",
        customMessageLabel: "#9575cd",
        toolPendingBg: "toolPendingBg",
        toolSuccessBg: "toolSuccessBg",
        toolErrorBg: "toolErrorBg",
        toolTitle: "text",
        toolOutput: "gray",
        toolError: "#ff5555",
        mdHeading: "#f0c674",
        mdLink: "#81a2be",
        mdLinkUrl: "dimGray",
        mdCode: "accent",
        mdCodeBlock: "green",
        mdCodeBlockBorder: "gray",
        mdQuote: "gray",
        mdQuoteBorder: "gray",
        mdHr: "gray",
        mdListBullet: "accent",
        toolDiffAdded: "green",
        toolDiffRemoved: "red",
        toolDiffContext: "gray",
        syntaxComment: "#6A9955",
        syntaxKeyword: "#569CD6",
        syntaxFunction: "#DCDCAA",
        syntaxVariable: "#9CDCFE",
        syntaxString: "#CE9178",
        syntaxNumber: "#B5CEA8",
        syntaxType: "#4EC9B0",
        syntaxOperator: "#D4D4D4",
        syntaxPunctuation: "#D4D4D4",
        thinkingOff: "darkGray",
        thinkingMinimal: "#6e6e6e",
        thinkingLow: "#5f87af",
        thinkingMedium: "#81a2be",
        thinkingHigh: "#b294bb",
        thinkingXhigh: "#d183e8",
        bashMode: "green",
    },
};

export const LIGHT_THEME: ThemeJson = {
    name: "light",
    vars: {
        teal: "#5a8080",
        blue: "#547da7",
        green: "#588458",
        red: "#aa5555",
        yellow: "#9a7326",
        text: "#1f2328",
        mediumGray: "#6c6c6c",
        dimGray: "#767676",
        lightGray: "#b0b0b0",
        selectedBg: "#d0d0e0",
        userMsgBg: "#e8e8e8",
        toolPendingBg: "#e8e8f0",
        toolSuccessBg: "#e8f0e8",
        toolErrorBg: "#f0e8e8",
        customMsgBg: "#ede7f6",
    },
    colors: {
        accent: "teal",
        border: "blue",
        borderAccent: "teal",
        borderMuted: "lightGray",
        success: "green",
        error: "red",
        warning: "yellow",
        muted: "mediumGray",
        dim: "dimGray",
        text: "text",
        thinkingText: "mediumGray",
        selectedBg: "selectedBg",
        userMessageBg: "userMsgBg",
        userMessageText: "text",
        customMessageBg: "customMsgBg",
        customMessageText: "text",
        customMessageLabel: "#7e57c2",
        toolPendingBg: "toolPendingBg",
        toolSuccessBg: "toolSuccessBg",
        toolErrorBg: "toolErrorBg",
        toolTitle: "text",
        toolOutput: "mediumGray",
        toolError: "#d70000",
        mdHeading: "yellow",
        mdLink: "blue",
        mdLinkUrl: "dimGray",
        mdCode: "teal",
        mdCodeBlock: "green",
        mdCodeBlockBorder: "mediumGray",
        mdQuote: "mediumGray",
        mdQuoteBorder: "mediumGray",
        mdHr: "mediumGray",
        mdListBullet: "green",
        toolDiffAdded: "green",
        toolDiffRemoved: "red",
        toolDiffContext: "mediumGray",
        syntaxComment: "#008000",
        syntaxKeyword: "#0000FF",
        syntaxFunction: "#795E26",
        syntaxVariable: "#001080",
        syntaxString: "#A31515",
        syntaxNumber: "#098658",
        syntaxType: "#267F99",
        syntaxOperator: "#000000",
        syntaxPunctuation: "#000000",
        thinkingOff: "lightGray",
        thinkingMinimal: "#767676",
        thinkingLow: "blue",
        thinkingMedium: "teal",
        thinkingHigh: "#875f87",
        thinkingXhigh: "#8b008b",
        bashMode: "green",
    },
};
