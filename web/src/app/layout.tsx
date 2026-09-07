import type { Metadata } from "next";
import localFont from "next/font/local";
import { ThemeProvider } from "@/components/theme-provider";
import { LocaleProvider } from "@/components/locale-provider";
import { AuthGuard } from "@/components/auth-guard";
import { AppShell } from "@/components/app-shell";
import "./globals.css";
import { cn } from "@/lib/utils";

const figtreeHeading = localFont({
  src: "./fonts/Figtree.woff2",
  variable: "--font-heading",
  weight: "400 900",
});

const nunitoSans = localFont({
  src: "./fonts/NunitoSans.woff2",
  variable: "--font-sans",
  weight: "200 900",
});

const geistSans = localFont({
  src: "./fonts/GeistVF.woff",
  variable: "--font-geist-sans",
  weight: "100 900",
});

const geistMono = localFont({
  src: "./fonts/GeistMonoVF.woff",
  variable: "--font-geist-mono",
  weight: "100 900",
});

export const metadata: Metadata = {
  title: "FastClaw",
  description: "AI Agent Framework",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" suppressHydrationWarning className={cn("font-sans", nunitoSans.variable, figtreeHeading.variable)}>
      <head>
        <script
          dangerouslySetInnerHTML={{
            __html: `(function(){try{var t=localStorage.getItem('fastclaw-theme');if(t==='dark'||(t==='system'&&window.matchMedia('(prefers-color-scheme: dark)').matches)){document.documentElement.classList.add('dark')}}catch(e){}})()`,
          }}
        />
      </head>
      <body
        className={`${geistSans.variable} ${geistMono.variable} antialiased`}
      >
        <LocaleProvider>
          <ThemeProvider><AuthGuard><AppShell>{children}</AppShell></AuthGuard></ThemeProvider>
        </LocaleProvider>
      </body>
    </html>
  );
}
