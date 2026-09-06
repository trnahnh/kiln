package com.github.trnahnh.kiln.audit.requests;

public class AdmissionRejectedException extends RuntimeException {

    private final int status;

    public AdmissionRejectedException(int status, String message, Throwable cause) {
        super(message, cause);
        this.status = status;
    }

    public int status() {
        return status;
    }
}
