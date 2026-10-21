"use client";

import * as React from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { DayPicker } from "react-day-picker";

import { cn } from "@/lib/utils";

function Calendar({
  className,
  classNames,
  showOutsideDays = true,
  ...props
}: React.ComponentProps<typeof DayPicker>) {
  return (
    <DayPicker
      showOutsideDays={showOutsideDays}
      className={cn("p-1 text-sm", className)}
      classNames={{
        months: "flex flex-col gap-4 sm:flex-row",
        month: "relative",
        month_caption: "flex h-9 items-center justify-center font-medium",
        nav: "absolute inset-x-2 top-2 flex items-center justify-between",
        button_previous: "inline-flex size-8 cursor-pointer items-center justify-center rounded-lg hover:bg-secondary text-muted-foreground hover:text-foreground",
        button_next: "inline-flex size-8 cursor-pointer items-center justify-center rounded-lg hover:bg-secondary text-muted-foreground hover:text-foreground",
        weekdays: "mt-2 grid grid-cols-7 text-xs text-muted-foreground",
        weekday: "flex h-8 items-center justify-center font-normal",
        week: "grid grid-cols-7",
        day: "relative size-9 p-0 text-center",
        day_button: "size-9 cursor-pointer rounded-lg text-sm transition hover:bg-secondary text-foreground disabled:cursor-not-allowed",
        today: "font-semibold text-foreground underline underline-offset-4",
        // 单日选中 / 区间端点：主色填充，对比鲜明
        selected: "[&>button]:bg-primary [&>button]:text-primary-foreground [&>button]:hover:bg-primary/90",
        // 区间中段：次要背景铺满，端点和中段自然拼接
        range_middle:
          "[&>button]:!rounded-none [&>button]:!bg-secondary [&>button]:!text-secondary-foreground [&>button]:hover:!bg-secondary/80",
        range_start: "[&>button]:!rounded-r-none",
        range_end: "[&>button]:!rounded-l-none",
        outside: "text-muted-foreground/40",
        disabled: "text-muted-foreground/30 opacity-50",
        ...classNames,
      }}
      components={{
        Chevron: ({ orientation }) =>
          orientation === "left" ? <ChevronLeft className="size-4" /> : <ChevronRight className="size-4" />,
      }}
      {...props}
    />
  );
}

export { Calendar };
