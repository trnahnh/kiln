package com.github.trnahnh.kiln.audit;

import org.jspecify.annotations.NonNull;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;

@SpringBootApplication
public class AuditServiceApplication {

    public static void main(String @NonNull [] args) {
        SpringApplication.run(AuditServiceApplication.class, args);
    }
}
